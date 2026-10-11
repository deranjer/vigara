#!/usr/bin/env python3
"""Validate Compose management examples and schema edge cases.

Requires Python 3.10+, PyYAML 6.x, and jsonschema 4.x.
Run from the repository root: python scripts/validate_compose_contracts.py
"""
import hashlib
import json
import posixpath
from pathlib import Path

import jsonschema
import yaml

ROOT = Path(__file__).resolve().parents[1]
SCHEMAS = ROOT / "schemas/compose-management"
EXAMPLES = ROOT / "examples/compose-management"


def fail(condition, message):
    if condition:
        raise AssertionError(message)


def validate(schema, data):
    return list(jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker()).iter_errors(data))


def frame(value):
    return len(value).to_bytes(8, "big") + value


def release_id(data):
    service_images = sorted(((item["service"].encode("utf-8"), item["digest"].encode("ascii")) for item in data["images"]), key=lambda pair: pair)
    payload = b"VIGARA-RELEASE-V1\0"
    payload += frame(data["assignmentId"].encode("utf-8"))
    payload += frame(data["gitCommit"].encode("ascii"))
    payload += frame(data["configurationRevision"].encode("ascii"))
    payload += len(service_images).to_bytes(8, "big")
    for service, digest in service_images:
        payload += frame(service) + frame(digest)
    payload += frame(data["jobId"].encode("utf-8"))
    return hashlib.sha256(payload).hexdigest()


def resolve_symlink_target(link_path, target):
    if not isinstance(target, str) or not target or "\x00" in target or "\\" in target or posixpath.isabs(target):
        raise ValueError("symlink target must be a nonempty relative POSIX path")
    resolved = posixpath.normpath(posixpath.join(posixpath.dirname(link_path), target))
    if resolved in {"", ".", ".."} or resolved.startswith("../"):
        raise ValueError("symlink target escapes the captured stack tree")
    return resolved


def check_symlink_cycles(edges):
    for link_path in edges:
        current_path = link_path
        visited_links = set()
        while True:
            components = current_path.split("/")
            link_prefix = next(("/".join(components[:index]) for index in range(1, len(components) + 1) if "/".join(components[:index]) in edges), None)
            if link_prefix is None:
                break
            if link_prefix in visited_links:
                raise ValueError("symlink cycle")
            visited_links.add(link_prefix)
            target = resolve_symlink_target(link_prefix, edges[link_prefix])
            suffix = components[len(link_prefix.split("/")):]
            current_path = posixpath.normpath(posixpath.join(target, *suffix))
            if current_path in {"", ".", ".."} or current_path.startswith("../"):
                raise ValueError("symlink chain escapes the captured stack tree")


def check_semantics(data):
    kind = data["kind"]
    if kind in {"Stack", "DeploymentAssignment"}:
        inputs = data["compose"]["interpolationFiles"] if kind == "Stack" else data.get("interpolationFiles", [])
        if len({item["path"] for item in inputs}) != len(inputs):
            raise ValueError("duplicate interpolation path")
        if kind == "DeploymentAssignment" and any(item["purpose"] != "deployment" for item in inputs):
            raise ValueError("assignment interpolation inputs must have purpose deployment")
        encrypted = any(item["sensitivity"] == "encrypted" for item in inputs)
        if encrypted and "secretRecipientRefs" not in data:
            raise ValueError("encrypted interpolation inputs require all secret recipient references")
        if "secretRecipientRefs" in data:
            classes = [item["class"] for item in data["secretRecipientRefs"]]
            expected = {"central-editor", "target-agent", "operator-recovery"}
            if set(classes) != expected or len(classes) != len(expected):
                raise ValueError("secret recipients must name each independent recipient class once")
    if kind == "OwnershipTransfer":
        if data["newEpoch"] != data["previousEpoch"] + 1:
            raise ValueError("ownership epoch must increment by one")
        if data["previousAgentId"] == data["newAgentId"]:
            raise ValueError("ownership transfer requires a distinct replacement agent")
    if kind == "Release":
        if data["releaseId"] != release_id(data):
            raise ValueError("releaseId does not match canonical identity")


def check_merged_inputs(stack, assignment):
    merged = stack["compose"]["interpolationFiles"] + assignment.get("interpolationFiles", [])
    paths = [item["path"] for item in merged]
    if len(paths) != len(set(paths)):
        raise ValueError("duplicate path in merged stack/assignment interpolation list")
    return merged


def must_reject_semantics(data, label):
    try:
        check_semantics(data)
    except ValueError:
        return
    raise AssertionError(f"semantic negative case accepted: {label}")


def must_reject_merged(stack, assignment, label):
    try:
        check_merged_inputs(stack, assignment)
    except ValueError:
        return
    raise AssertionError(f"merged-input negative case accepted: {label}")


def main():
    names = {"Stack": "stack", "ServerProfile": "server-profile", "DeploymentAssignment": "assignment", "Release": "release", "RuntimeBinding": "runtime-binding", "OwnershipTransfer": "ownership-transfer"}
    schemas = {kind: json.loads((SCHEMAS / f"{file}.schema.json").read_text()) for kind, file in names.items()}
    for schema in schemas.values():
        jsonschema.Draft202012Validator.check_schema(schema)

    docs = {}
    manifest_paths = sorted(path for path in EXAMPLES.rglob("*.yaml") if path.name != "compose.yaml")
    for path in manifest_paths:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
        kind = data.get("kind")
        fail(kind not in schemas, f"no schema for {path}: {kind}")
        errors = validate(schemas[kind], data)
        fail(bool(errors), f"invalid example {path}: {errors}")
        check_semantics(data)
        docs[path] = data

    stacks = {data["stackId"]: data for data in docs.values() if data["kind"] == "Stack"}
    for assignment_doc in (data for data in docs.values() if data["kind"] == "DeploymentAssignment"):
        stack_doc = stacks[assignment_doc["stackId"]]
        check_merged_inputs(stack_doc, assignment_doc)

    stack = next(v for v in docs.values() if v["kind"] == "Stack")
    assignment = next(v for v in docs.values() if v["kind"] == "DeploymentAssignment" and v["ownership"]["mode"] == "exclusive")
    release = next(v for v in docs.values() if v["kind"] == "Release")
    transfer = next(v for v in docs.values() if v["kind"] == "OwnershipTransfer")

    def must_reject(schema, value, label):
        fail(not validate(schema, value), f"negative case accepted: {label}")

    stack_validator = schemas["Stack"]
    for path in (".", "..", "../outside", "config/../outside", "config/.", "/outside"):
        bad = json.loads(json.dumps(stack)); bad["compose"]["files"] = [path]
        must_reject(stack_validator, bad, f"Compose traversal {path}")
        bad = json.loads(json.dumps(stack)); bad["compose"]["interpolationFiles"] = [{"path": path, "purpose": "stack", "sensitivity": "encrypted"}]
        must_reject(stack_validator, bad, f"interpolation traversal {path}")
    bad = json.loads(json.dumps(stack)); bad["configurationAssets"] = [{"path": "../../etc/passwd", "class": "portable-config", "required": True}]
    must_reject(stack_validator, bad, "portable configuration asset traversal")
    bad = json.loads(json.dumps(stack)); bad["secretRecipientRefs"] = bad["secretRecipientRefs"][:2]
    must_reject_semantics(bad, "missing secret recovery recipient")
    bad = json.loads(json.dumps(stack)); del bad["secretRecipientRefs"]
    must_reject_semantics(bad, "missing recipient refs for encrypted inputs")

    assignment_validator = schemas["DeploymentAssignment"]
    bad = json.loads(json.dumps(assignment)); bad["ownership"]["mode"] = "stateless"
    must_reject(assignment_validator, bad, "stateless assignment without replicaKey")
    bad = json.loads(json.dumps(assignment)); bad["replicaKey"] = "replica-a"
    must_reject(assignment_validator, bad, "exclusive assignment with replicaKey")
    bad = json.loads(json.dumps(assignment)); bad["interpolationFiles"] = [{"path": "shared.env", "purpose": "deployment", "sensitivity": "plaintext"}, {"path": "shared.env", "purpose": "deployment", "sensitivity": "plaintext"}]
    must_reject_semantics(bad, "duplicate assignment interpolation path")
    bad = json.loads(json.dumps(assignment)); bad["interpolationFiles"] = [{"path": "secrets/private.env.sops", "purpose": "deployment", "sensitivity": "encrypted"}]
    must_reject(assignment_validator, bad, "encrypted assignment inputs without recipient policy")
    bad = json.loads(json.dumps(assignment)); bad["interpolationFiles"] = [{"path": "../outside", "purpose": "deployment", "sensitivity": "encrypted"}]; bad["secretRecipientRefs"] = json.loads(json.dumps(next(v for v in docs.values() if v["kind"] == "DeploymentAssignment" and v.get("secretRecipientRefs"))["secretRecipientRefs"]))
    must_reject(assignment_validator, bad, "assignment path traversal")

    # Precedence is array order. A path may occur only once, even with conflicting declarations.
    duplicate_inputs = [{"path": "shared.env.sops", "purpose": "shared", "sensitivity": "encrypted"}, {"path": "shared.env.sops", "purpose": "deployment", "sensitivity": "plaintext"}]
    bad = json.loads(json.dumps(stack)); bad["compose"]["interpolationFiles"] = duplicate_inputs
    must_reject_semantics(bad, "duplicate interpolation precedence")
    fail("precedence" in stack["compose"]["interpolationFiles"][0], "unexpected numeric precedence fixture")
    assignment_secret = json.loads(json.dumps(next(v for v in docs.values() if v["kind"] == "DeploymentAssignment" and v.get("interpolationFiles"))))
    stack_for_assignment = stacks[assignment_secret["stackId"]]
    bad = json.loads(json.dumps(assignment_secret)); bad["interpolationFiles"][0]["path"] = stack_for_assignment["compose"]["interpolationFiles"][0]["path"]
    must_reject_merged(stack_for_assignment, bad, "duplicate stack+assignment precedence")

    release_schema = schemas["Release"]
    for field, value in (("gitCommit", "a" * 41), ("gitCommit", "g" * 40), ("configurationRevision", "sha256:" + "z" * 64)):
        bad = json.loads(json.dumps(release)); bad[field] = value
        must_reject(release_schema, bad, f"bad {field}")
    bad = json.loads(json.dumps(release)); bad["images"][0]["digest"] = "sha256:" + "z" * 64
    must_reject(release_schema, bad, "malformed image digest")

    transfer_schema = schemas["OwnershipTransfer"]
    fail(transfer["newEpoch"] != transfer["previousEpoch"] + 1, "example transfer epoch must increment by one")
    bad = json.loads(json.dumps(transfer)); bad["oldOwnerFenced"] = False
    must_reject(transfer_schema, bad, "unfenced transfer")
    bad = json.loads(json.dumps(transfer)); bad["gitCommit"] = "a" * 41
    must_reject(transfer_schema, bad, "malformed transfer Git OID")
    stale = json.loads(json.dumps(transfer)); stale["newEpoch"] = stale["previousEpoch"]
    must_reject_semantics(stale, "stale transfer epoch")
    same_owner = json.loads(json.dumps(transfer)); same_owner["newAgentId"] = same_owner["previousAgentId"]
    must_reject_semantics(same_owner, "same-agent transfer")

    assert resolve_symlink_target("config/link", "../secrets/runtime.env.sops") == "secrets/runtime.env.sops"
    for target in ("/etc/passwd", "../../outside", "..\\outside"):
        try:
            resolve_symlink_target("config/link", target)
        except ValueError:
            continue
        raise AssertionError(f"unsafe symlink target accepted: {target}")
    for edges in ({"a": "b", "b": "a"}, {"dirlink": "real", "real/back": "../dirlink/back"}):
        try:
            check_symlink_cycles(edges)
        except ValueError:
            continue
        raise AssertionError(f"symlink cycle accepted: {edges}")

    print(f"Validated {len(schemas)} schemas and {len(docs)} YAML manifests; negative cases passed.")


if __name__ == "__main__":
    main()
