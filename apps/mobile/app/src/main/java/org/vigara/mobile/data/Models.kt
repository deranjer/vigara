package org.vigara.mobile.data

import kotlinx.serialization.Serializable

@Serializable data class User(val id: String = "", val username: String = "", val displayName: String = "", val role: String = "viewer") {
    val canOperate get() = role == "operator" || role == "admin"
    val isAdmin get() = role == "admin"
}
@Serializable data class UserEnvelope(val user: User = User())
@Serializable data class SetupStatus(val setupRequired: Boolean = false)
@Serializable data class VersionInfo(val version: String = "")

// ---- Devices & monitoring ----
@Serializable data class Metric(val name: String = "", val value: Double = 0.0, val unit: String = "", val labels: Map<String, String> = emptyMap())
@Serializable data class HostInfo(
    val os: String = "", val osVersion: String = "", val architecture: String = "", val agentVersion: String = "",
    val cpuModel: String = "", val logicalCpus: Int = 0, val totalMemoryBytes: Long = 0, val uptimeSeconds: Long = 0,
)
@Serializable data class CollectionError(val capability: String = "", val message: String = "")
@Serializable data class Snapshot(
    val observedAt: String? = null, val host: HostInfo? = null, val metrics: List<Metric> = emptyList(),
    val errors: List<CollectionError> = emptyList(),
)
@Serializable data class Device(
    val id: String = "", val name: String = "", val status: String = "unsupported", val agentId: String? = null,
    val platform: String = "", val architecture: String = "", val agentVersion: String = "",
    val capabilities: List<String> = emptyList(), val lastSeenAt: String? = null, val latest: Snapshot? = null,
    val telemetryObservedAt: String? = null, val collectionErrors: List<CollectionError> = emptyList(),
)
@Serializable data class DevicePage(val devices: List<Device> = emptyList(), val total: Int = 0, val offset: Int = 0, val limit: Int = 0)
@Serializable data class Point(val at: String = "", val value: Double? = null)
@Serializable data class Series(val name: String = "", val unit: String = "", val labels: Map<String, String> = emptyMap(), val points: List<Point> = emptyList())
@Serializable data class History(val range: String = "", val metrics: List<String> = emptyList(), val series: List<Series> = emptyList())
@Serializable data class Incident(
    val id: String = "", val dimension: String = "", val status: String = "", val summary: String = "",
    val startedAt: String = "", val lastObservedAt: String? = null, val resolvedAt: String? = null,
)
@Serializable data class Awake(val weekdays: List<String> = emptyList(), val start: String = "", val end: String = "", val timezone: String = "")
@Serializable data class MaintenanceWindow(val id: String = "", val startsAt: String = "", val endsAt: String = "")
@Serializable data class Availability(
    val awake: Awake? = null, val expectedAwake: Boolean = true, val maintenanceActive: Boolean = false,
    val maintenance: List<MaintenanceWindow> = emptyList(),
)

// ---- Backups ----
@Serializable data class BackupPolicy(
    val id: String = "", val deviceId: String = "", val name: String = "", val profileId: String = "",
    val destination: String = "", val includedPaths: List<String> = emptyList(), val enabled: Boolean = true,
)
@Serializable data class BackupProgress(val phase: String = "", val percentDone: Double? = null, val filesDone: Long? = null, val filesTotal: Long? = null)
@Serializable data class BackupJob(
    val id: String = "", val scheduledFor: String = "", val status: String = "", val executionStatus: String = "",
    val outcome: String = "", val errorMessage: String = "", val createdAt: String = "", val progress: BackupProgress? = null,
)

// ---- Containers ----
@Serializable data class DockerPort(val ip: String = "", val privatePort: Int = 0, val publicPort: Int = 0, val type: String = "")
@Serializable data class ContainerInfo(
    val id: String = "", val name: String = "", val image: String = "", val state: String = "", val status: String = "",
    val created: Long = 0, val ports: List<DockerPort> = emptyList(), val project: String = "", val service: String = "",
)
@Serializable data class HostEntry(
    val agentId: String = "", val name: String = "", val state: String = "offline", val message: String = "",
    val dockerTier: String = "read", val containers: List<ContainerInfo> = emptyList(), val truncated: Boolean = false,
) {
    fun canManage(user: User) = user.canOperate && tierRank(dockerTier) >= 1
    fun canExec(user: User) = user.canOperate && tierRank(dockerTier) >= 2
    fun canDeploy(user: User) = user.canOperate && tierRank(dockerTier) >= 3
}
fun tierRank(tier: String) = when (tier) { "manage" -> 1; "exec" -> 2; "deploy" -> 3; else -> 0 }
@Serializable data class Fleet(val hosts: List<HostEntry> = emptyList())
data class FleetRow(val host: HostEntry, val container: ContainerInfo)

@Serializable data class ContainerState(
    val status: String = "", val running: Boolean = false, val paused: Boolean = false, val restarting: Boolean = false,
    val oomKilled: Boolean = false, val exitCode: Int = 0, val error: String = "", val startedAt: String = "",
    val finishedAt: String = "", val health: String = "",
)
@Serializable data class EnvVar(val name: String = "", val value: String? = null)
@Serializable data class MountInfo(val type: String = "", val name: String = "", val source: String = "", val destination: String = "", val readWrite: Boolean = true)
@Serializable data class NetworkAttachment(val name: String = "", val ipAddress: String = "", val gateway: String = "", val macAddress: String = "")
@Serializable data class InspectResult(
    val id: String = "", val name: String = "", val image: String = "", val created: String = "", val state: ContainerState = ContainerState(),
    val command: List<String> = emptyList(), val entrypoint: List<String> = emptyList(), val workingDir: String = "", val user: String = "",
    val hostname: String = "", val env: List<EnvVar> = emptyList(), val envRevealed: Boolean = false, val labels: Map<String, String> = emptyMap(),
    val mounts: List<MountInfo> = emptyList(), val ports: List<DockerPort> = emptyList(), val networks: List<NetworkAttachment> = emptyList(),
    val restartPolicy: String = "", val networkMode: String = "", val privileged: Boolean = false, val readonlyRoot: Boolean = false,
    val memoryLimit: Long = 0, val capAdd: List<String> = emptyList(), val logDriver: String = "", val restartCount: Int = 0,
)
@Serializable data class ActionResult(val id: String = "", val name: String = "", val action: String = "", val changed: Boolean = false, val message: String = "")
@Serializable data class LogLine(val s: String = "o", val t: String = "", val m: String = "", val x: Boolean = false)
@Serializable data class LogSnapshot(val lines: List<LogLine> = emptyList(), val truncated: Boolean = false)
@Serializable data class StatsSample(
    val t: String = "", val cpu: Double = 0.0, val cpuCores: Int = 0, val memUsed: Long = 0, val memLimit: Long = 0, val memPct: Double = 0.0,
    val netRxBps: Double = 0.0, val netTxBps: Double = 0.0, val blkRead: Long = 0, val blkWrite: Long = 0, val pids: Int = 0,
)

// ---- Docker resources ----
@Serializable data class UsedBy(val id: String = "", val name: String = "")
@Serializable data class ImageRow(val id: String = "", val tags: List<String> = emptyList(), val created: Long = 0, val size: Long = 0, val dangling: Boolean = false, val usedBy: List<UsedBy> = emptyList())
@Serializable data class NetworkRow(val id: String = "", val name: String = "", val driver: String = "", val scope: String = "", val internal: Boolean = false, val builtin: Boolean = false, val subnets: List<String> = emptyList(), val usedBy: List<UsedBy> = emptyList())
@Serializable data class VolumeRow(val name: String = "", val driver: String = "", val mountpoint: String = "", val size: Long = 0, val anonymous: Boolean = false, val usedBy: List<UsedBy> = emptyList())
@Serializable data class ImageList(val images: List<ImageRow>? = null)
@Serializable data class NetworkList(val networks: List<NetworkRow>? = null)
@Serializable data class VolumeList(val volumes: List<VolumeRow>? = null)
@Serializable data class PruneItem(val id: String = "", val name: String = "", val size: Long = 0)
@Serializable data class PruneResult(val dryRun: Boolean = false, val candidates: List<PruneItem> = emptyList(), val removed: List<String> = emptyList(), val spaceReclaimed: Long = 0)

// ---- Stacks ----
@Serializable data class StackSummary(
    val id: String = "", val name: String = "", val projectName: String = "", val commit: String = "", val valid: Boolean = true,
    val assigned: Boolean = false, val agentId: String = "", val deviceName: String = "", val serviceCount: Int = 0,
    val imported: Boolean = false, val digest: String = "", val deploymentDigest: String = "", val deploymentStatus: String = "",
    val deploymentCommit: String = "", val deploymentAt: String = "",
)
@Serializable data class StackList(val stacks: List<StackSummary> = emptyList())
@Serializable data class StackSnapshot(val id: String = "", val commit: String = "", val files: Map<String, String> = emptyMap())
@Serializable data class StackDraft(val base: String = "", val files: Map<String, String>? = null, val version: Int = 0)
@Serializable data class StackDetail(
    val projectName: String = "", val snapshot: StackSnapshot = StackSnapshot(), val draft: StackDraft = StackDraft(),
    val branch: String = "", val assigned: Boolean = false, val agentId: String = "",
)
@Serializable data class StackRevision(val commit: String = "", val message: String = "", val at: String = "")
@Serializable data class StackDeployment(val id: String = "", val commit: String = "", val status: String = "", val stage: String = "", val message: String = "", val createdAt: String = "")
@Serializable data class StackPlan(val images: Map<String, String> = emptyMap(), val token: String = "", val projectName: String = "", val services: List<String> = emptyList())
@Serializable data class MessageResult(val message: String = "", val state: String = "", val commit: String = "")

// ---- Image updates ----
@Serializable data class UpdateResult(
    val runningImage: String = "", val name: String = "", val image: String = "", val status: String = "", val message: String = "",
    val current: String = "", val available: String = "",
)
@Serializable data class UpdateItem(
    val trackingTag: String = "", val agentId: String = "", val device: String = "", val containerId: String = "", val stackId: String = "",
    val result: UpdateResult = UpdateResult(), val checkedAt: String = "", val ignored: Boolean = false,
)
@Serializable data class UpdateHost(val agentId: String = "", val device: String = "", val checkedAt: String? = null, val nextCheck: String = "", val message: String = "")
@Serializable data class UpdateOverview(val items: List<UpdateItem> = emptyList(), val hosts: List<UpdateHost> = emptyList())

// ---- Admin ----
@Serializable data class ManagedUser(val id: String = "", val username: String = "", val displayName: String = "", val role: String = "viewer", val createdAt: String = "")
@Serializable data class ScheduledTask(
    val id: String = "", val name: String = "", val description: String = "", val available: Boolean = true, val enabled: Boolean = false,
    val intervalSeconds: Long = 0, val runOnStartup: Boolean = false, val status: String = "", val lastStartedAt: String? = null,
    val lastFinishedAt: String? = null, val nextRunAt: String? = null, val lastError: String = "",
)
@Serializable data class AuditEntry(
    val id: Long = 0, val action: String = "", val outcome: String = "", val actor: String = "", val actorName: String = "",
    val target: String = "", val at: String = "", val createdAt: String = "",
)

// ---- Managed backups ----
@Serializable data class BackupSchedule(val weekdays: List<String> = emptyList(), val time: String = "02:00", val timezone: String = "UTC")
@Serializable data class ManagedConfig(
    val repository: String = "", val serverRepository: String = "", val sourcePaths: List<String> = emptyList(), val password: String? = null,
    val initialize: Boolean = true, val schedule: BackupSchedule = BackupSchedule(), val enabled: Boolean = true, val expectedRevision: Int = 0,
)
@Serializable data class ManagedView(
    val configured: Boolean = false, val initialized: Boolean = false, val available: Boolean = false, val revision: Int = 0,
    val appliedRevision: Int = 0, val errorCode: String = "", val reportedAt: String = "", val config: ManagedConfig? = null,
)
@Serializable data class BackupSnapshot(val id: String = "", val time: String = "", val hostname: String = "", val paths: List<String> = emptyList())
@Serializable data class SnapshotEntry(val name: String = "", val type: String = "", val path: String = "", val size: Long = 0)

// ---- Alert rules ----
@Serializable data class RuleConfig(
    val deviceId: String = "", val staleAfterSeconds: Int = 0, val metric: String = "", val operator: String = "",
    val threshold: Double = 0.0, val sustainSeconds: Int = 0, val graceSeconds: Int = 0, val policyKey: String = "", val intervalSeconds: Int = 0,
)
@Serializable data class AlertRule(val id: String = "", val name: String = "", val kind: String = "", val enabled: Boolean = true, val config: RuleConfig = RuleConfig(), val createdAt: String = "")

// ---- Stack setup, import and Git ----
@Serializable data class SetupDevice(val agentId: String = "", val name: String = "", val managedRoot: String = "", val message: String = "")
@Serializable data class SetupDevices(val devices: List<SetupDevice> = emptyList())
@Serializable data class StackListing(val configured: Boolean = true, val branch: String = "", val stacks: List<StackSummary> = emptyList())
@Serializable data class Candidate(
    val directory: String = "", val projectName: String = "", val token: String = "", val state: String = "", val containers: Int = 0,
    val composeFiles: List<String> = emptyList(), val environmentFiles: List<String> = emptyList(), val issues: List<String> = emptyList(),
    val imported: Boolean = false, val storage: List<String> = emptyList(),
)
@Serializable data class Discovery(val root: String = "", val candidates: List<Candidate> = emptyList())
@Serializable data class ImportPreview(val candidate: Candidate = Candidate(), val files: Map<String, String> = emptyMap())
@Serializable data class CreatedStack(val id: String = "", val state: String = "")
@Serializable data class DraftResult(val digest: String = "", val base: String = "", val files: Map<String, String>? = null, val version: Int = 0)
@Serializable data class SaveResult(val state: String = "", val commit: String = "")
@Serializable data class Encrypted(val path: String = "", val ciphertext: String = "", val manifest: String = "")
@Serializable data class Revealed(val value: String = "")
@Serializable data class GitHubStatus(val available: Boolean = false, val connected: Boolean = false, val pending: Boolean = false, val canManage: Boolean = false, val setupHelp: String = "", val slug: String = "", val remote: String = "", val branch: String = "")
@Serializable data class GitSettings(val remote: String = "", val branch: String = "", val version: Int = 0, val authMode: String = "", val githubApp: GitHubStatus = GitHubStatus())

// ---- Enrollment and MCP ----
@Serializable data class ServerOption(val url: String = "", val secure: Boolean = true)
@Serializable data class DownloadAvailability(
    val serverUrl: String = "", val serverUrls: List<ServerOption> = emptyList(), val allowInsecureHttp: Boolean = false,
    val targets: List<String> = emptyList(), val managedBackupsAvailable: Boolean = false,
)
@Serializable data class EnrollmentStatus(val status: String = "")
@Serializable data class McpStatus(val enabled: Boolean = false, val serverUrl: String = "")
