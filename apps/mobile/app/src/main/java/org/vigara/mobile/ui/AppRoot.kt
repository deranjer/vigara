package org.vigara.mobile.ui

import android.net.Uri
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Build
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.filled.List
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.NavHostController
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.Incident
import org.vigara.mobile.data.User
import org.vigara.mobile.data.UserEnvelope
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.screens.AccountScreen
import org.vigara.mobile.ui.screens.AdminScreen
import org.vigara.mobile.ui.screens.AlertsScreen
import org.vigara.mobile.ui.screens.ContainerDetailScreen
import org.vigara.mobile.ui.screens.ContainersScreen
import org.vigara.mobile.ui.screens.DeviceDetailScreen
import org.vigara.mobile.ui.screens.DevicesScreen
import org.vigara.mobile.ui.screens.ImageUpdatesScreen
import org.vigara.mobile.ui.screens.LoginScreen
import org.vigara.mobile.ui.screens.MoreScreen
import org.vigara.mobile.ui.screens.OverviewScreen
import org.vigara.mobile.ui.common.rememberLoad
import org.vigara.mobile.ui.screens.ResourcesScreen
import org.vigara.mobile.ui.screens.StackDetailScreen
import org.vigara.mobile.ui.screens.StacksScreen
import org.vigara.mobile.ui.screens.AddDeviceScreen
import org.vigara.mobile.ui.screens.AlertRulesScreen
import org.vigara.mobile.ui.screens.GitSettingsScreen
import org.vigara.mobile.ui.screens.McpScreen
import org.vigara.mobile.ui.screens.StackImportScreen
import org.vigara.mobile.ui.screens.WebScreen
import org.vigara.mobile.ui.screens.WebTarget

private data class Tab(val route: String, val label: String, val icon: ImageVector)

private val tabs = listOf(
    Tab("overview", "Overview", Icons.Default.Home),
    Tab("containers", "Containers", Icons.Default.List),
    Tab("stacks", "Stacks", Icons.Default.Build),
    Tab("alerts", "Alerts", Icons.Default.Notifications),
    Tab("more", "More", Icons.Default.Menu),
)

@Composable
fun AppRoot(api: VigaraApi) {
    var user by remember { mutableStateOf<User?>(null) }
    var checking by remember { mutableStateOf(api.hasServer) }
    var startupError by remember { mutableStateOf<String?>(null) }
    val unauthorized by api.unauthorized.collectAsState()

    LaunchedEffect(Unit) {
        if (api.hasServer) {
            try {
                user = api.get<UserEnvelope>("/api/auth/me").user
            } catch (failure: ApiException) {
                if (failure.status != 401) startupError = failure.message
            }
        }
        checking = false
    }
    LaunchedEffect(unauthorized) { if (unauthorized > 0) user = null }

    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
        when {
            checking -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
            user == null -> LoginScreen(api, startupError) { signedIn -> startupError = null; user = signedIn }
            else -> MainShell(api, user!!, onUserChanged = { user = it }, onSignedOut = { user = null })
        }
    }
}

private fun NavHostController.goTab(route: String) {
    navigate(route) {
        popUpTo(graph.findStartDestination().id) { saveState = true }
        launchSingleTop = true
        restoreState = true
    }
}

@Composable
private fun MainShell(api: VigaraApi, user: User, onUserChanged: (User) -> Unit, onSignedOut: () -> Unit) {
    val nav = rememberNavController()
    var web by remember { mutableStateOf<Pair<String, WebTarget>?>(null) }
    // The Alerts tab carries the open count, as the web sidebar does.
    val openAlerts = rememberLoad(pollMs = 60_000) { runCatching { api.get<List<Incident>>("/api/alerts/incidents").count { it.bucket() == "open" } }.getOrDefault(0) }
    val tabRoutes = tabs.map { it.route }.toSet()
    val go = { route: String -> if (route in tabRoutes) nav.goTab(route) else nav.navigate(route) }
    val backStack by nav.currentBackStackEntryAsState()
    val currentRoute = backStack?.destination?.route.orEmpty()
    val groupOf = { route: String ->
        when {
            route == "overview" || route.startsWith("device/") -> "overview"
            route.startsWith("container") -> "containers"
            route.startsWith("stack") || route == "git-settings" -> "stacks"
            route.startsWith("alert") -> "alerts"
            else -> "more"
        }
    }

    Scaffold(
        contentWindowInsets = WindowInsets(0, 0, 0, 0),
        bottomBar = {
            NavigationBar {
                tabs.forEach { tab ->
                    NavigationBarItem(
                        selected = groupOf(currentRoute) == tab.route,
                        onClick = { nav.goTab(tab.route) },
                        icon = {
                            val count = openAlerts.data ?: 0
                            if (tab.route == "alerts" && count > 0) BadgedBox(badge = { Badge { Text(if (count > 99) "99+" else "$count") } }) { Icon(tab.icon, contentDescription = null) }
                            else Icon(tab.icon, contentDescription = null)
                        },
                        label = { Text(tab.label) },
                    )
                }
            }
        },
    ) { padding ->
        NavHost(nav, startDestination = "overview", modifier = Modifier.padding(padding)) {
            composable("overview") { OverviewScreen(api, onNavigate = go, onOpenDevice = { nav.navigate("device/${Uri.encode(it)}") }) }
            composable("devices") { DevicesScreen(api, user, onOpen = { nav.navigate("device/${Uri.encode(it)}") }, onAdd = { nav.navigate("add-device") }, onBack = { nav.popBackStack() }) }
            composable("device/{id}", listOf(navArgument("id") { type = NavType.StringType })) {
                DeviceDetailScreen(api, user, it.arguments?.getString("id").orEmpty(), onBack = { nav.popBackStack() })
            }
            composable("containers") {
                ContainersScreen(api, user, onOpen = { host, ref -> nav.navigate("container/${Uri.encode(host)}/${Uri.encode(ref)}") })
            }
            composable(
                "container/{host}/{ref}",
                listOf(navArgument("host") { type = NavType.StringType }, navArgument("ref") { type = NavType.StringType }),
            ) {
                ContainerDetailScreen(api, user, it.arguments?.getString("host").orEmpty(), it.arguments?.getString("ref").orEmpty(), onBack = { nav.popBackStack() })
            }
            composable("stacks") {
                StacksScreen(api, user, onOpen = { nav.navigate("stack/${Uri.encode(it)}") }, onImport = { nav.navigate("stack-import") }, onGit = { nav.navigate("git-settings") })
            }
            composable("stack-import") {
                StackImportScreen(api, onBack = { nav.popBackStack() }, onImported = { id -> nav.popBackStack(); nav.navigate("stack/${Uri.encode(id)}") })
            }
            composable("git-settings") {
                GitSettingsScreen(api, user, onBack = { nav.popBackStack() }, onWeb = { title, target -> web = title to target; nav.navigate("web") })
            }
            composable("web") { web?.let { (title, target) -> WebScreen(api, title, target, onBack = { nav.popBackStack() }) } }
            composable("alert-rules") { AlertRulesScreen(api, user, onBack = { nav.popBackStack() }) }
            composable("add-device") { AddDeviceScreen(api, user, onBack = { nav.popBackStack() }) }
            composable("mcp") {
                McpScreen(api, user, onBack = { nav.popBackStack() }, onManage = { web = "Connected applications" to WebTarget.ServerPath("/oauth/connections"); nav.navigate("web") })
            }
            composable("stack/{id}", listOf(navArgument("id") { type = NavType.StringType })) {
                StackDetailScreen(api, user, it.arguments?.getString("id").orEmpty(), onBack = { nav.popBackStack() },
                    onOpenContainer = { host, ref -> nav.navigate("container/${Uri.encode(host)}/${Uri.encode(ref)}") })
            }
            composable("updates") { ImageUpdatesScreen(api, user, onOpenContainer = { host, ref -> nav.navigate("container/${Uri.encode(host)}/${Uri.encode(ref)}") }, onBack = { nav.popBackStack() }) }
            composable("more") { MoreScreen(user, onNavigate = { nav.navigate(it) }) }
            composable("resources/{kind}", listOf(navArgument("kind") { type = NavType.StringType })) {
                ResourcesScreen(api, user, it.arguments?.getString("kind").orEmpty(), onBack = { nav.popBackStack() })
            }
            composable("alerts") { AlertsScreen(api, onOpenDevice = { nav.navigate("device/${Uri.encode(it)}") }, onRules = { nav.navigate("alert-rules") }) }
            composable("account") { AccountScreen(api, user, onBack = { nav.popBackStack() }, onUserChanged = onUserChanged, onSignedOut = onSignedOut) }
            composable("admin") { AdminScreen(api, user, onBack = { nav.popBackStack() }) }
        }
    }
}
