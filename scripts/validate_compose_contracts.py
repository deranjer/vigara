#!/usr/bin/env python3
"""Validate Compose management examples and schema edge cases.

Requires Python 3.10+, PyYAML 6.x, and jsonschema 4.x.
Run from the repository root: python scripts/validate_compose_contracts.py
"""
import hashlib
import json
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


def check_semantics(data):
    kind = data["kind"]
    if kind == "Stack":
        inputs = data["compose"]["interpolationFiles"]
        if len({item["path"] for item in inputs}) != len(inputs):
            raise ValueError("duplicate interpolation path")
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
        payload = {"assignmentId": data["assignmentId"], "gitCommit": data["gitCommit"], "configurationRevision": data["configurationRevision"], "serviceImages": sorted((x["service"], x["digest"]) for x in data["images"]), "jobId": data["jobId"]}
        raw = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
        if data["releaseId"] != hashlib.sha256(raw).hexdigest():
            raise ValueError("releaseId does not match canonical identity")


def must_reject_semantics(data, label):
    try:
        check_semantics(data)
    except ValueError:
        return
    raise AssertionError(f"semantic negative case accepted: {label}")


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

    stack = next(v for v in docs.values() if v["kind"] == "Stack")
    assignment = next(v for v in docs.values() if v["kind"] == "DeploymentAssignment")
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
    bad = json.loads(json.dumps(stack)); bad["secretRecipientRefs"] = bad["secretRecipientRefs"][:2]
    must_reject_semantics(bad, "missing secret recovery recipient")
    bad = json.loads(json.dumps(stack)); del bad["secretRecipientRefs"]
    must_reject_semantics(bad, "missing recipient refs for encrypted inputs")

    assignment_validator = schemas["DeploymentAssignment"]
    bad = json.loads(json.dumps(assignment)); bad["ownership"]["mode"] = "stateless"
    must_reject(assignment_validator, bad, "stateless assignment without replicaKey")
    bad = json.loads(json.dumps(assignment)); bad["replicaKey"] = "replica-a"
    must_reject(assignment_validator, bad, "exclusive assignment with replicaKey")
    bad = json.loads(json.dumps(assignment)); bad["interpolationFiles"] = ["shared.env", "shared.env"]
    must_reject(assignment_validator, bad, "duplicate assignment interpolation path")

    # Precedence is array order. A path may occur only once, even with conflicting declarations.
    duplicate_inputs = [{"path": "shared.env.sops", "purpose": "shared", "sensitivity": "encrypted"}, {"path": "shared.env.sops", "purpose": "deployment", "sensitivity": "plaintext"}]
    bad = json.loads(json.dumps(stack)); bad["compose"]["interpolationFiles"] = duplicate_inputs
    must_reject_semantics(bad, "duplicate interpolation precedence")
    fail("precedence" in stack["compose"]["interpolationFiles"][0], "unexpected numeric precedence fixture")

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

    print(f"Validated {len(schemas)} schemas and {len(docs)} YAML manifests; negative cases passed.")


if __name__ == "__main__":
    main()
