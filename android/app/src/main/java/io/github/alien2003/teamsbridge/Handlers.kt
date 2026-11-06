package io.github.alien2003.teamsbridge

import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ResolveInfo
import android.net.Uri
import android.os.Build
import io.github.alien2003.teamsbridge.core.HandlerKind

/**
 * The apps that can open a Teams link (BRIEF §6): browsers and installed Teams PWAs (WebAPKs).
 * Visible to us through the `<queries>` entry for VIEW/BROWSABLE/https in the manifest.
 */
object Handlers {
    class Handler(val packageName: String, val label: String, val kind: HandlerKind, val info: ResolveInfo)

    fun query(ctx: Context): List<Handler> {
        val pm = ctx.packageManager
        val byPackage = LinkedHashMap<String, Handler>()
        for (u in HandlerKind.QUERY_URIS) {
            val intent = Intent(Intent.ACTION_VIEW, Uri.parse(u)).addCategory(Intent.CATEGORY_BROWSABLE)
            val flags = PackageManager.MATCH_ALL or PackageManager.GET_RESOLVED_FILTER
            val infos = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                pm.queryIntentActivities(intent, PackageManager.ResolveInfoFlags.of(flags.toLong()))
            } else {
                @Suppress("DEPRECATION")
                pm.queryIntentActivities(intent, flags)
            }
            for (ri in infos) {
                val pkg = ri.activityInfo?.packageName ?: continue
                if (pkg == ctx.packageName) continue
                // The returned filter is the one that matched this https URI. If it names any host,
                // it matched on the Teams host, not merely on the scheme: an installed web app.
                val hostMatch = (ri.filter?.countDataAuthorities() ?: 0) > 0
                val kind = HandlerKind.classify(pkg, hostMatch)
                val known = byPackage[pkg]
                if (known == null || (known.kind == HandlerKind.BROWSER && kind != HandlerKind.BROWSER)) {
                    byPackage[pkg] = Handler(pkg, ri.loadLabel(pm).toString(), kind, ri)
                }
            }
        }
        return byPackage.values.sortedWith(compareBy({ it.kind.ordinal }, { it.label.lowercase() }))
    }
}
