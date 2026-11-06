# R8 rules shared by debug and release (both are minified: the debug APK must stay under 6 MB, see
# docs/DECISIONS.md). AndroidX, Material, Firebase and WorkManager ship consumer rules; AAPT2 keeps
# everything the manifest names (TbdApp, the activities, MessagingService, zxing's CaptureActivity);
# WorkManager's consumer rules keep ListenableWorker subclasses' (Context, WorkerParameters)
# constructor, which it calls reflectively for WatchdogWorker.
#
# Our code uses no reflection: JCA algorithms are looked up by string name, org.json is the
# platform's, and ScanContract / ScanOptions are called directly.
