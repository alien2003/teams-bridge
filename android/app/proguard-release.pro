# Release only: drop debug/verbose logging (it never contains message content anyway).
-assumenosideeffects class android.util.Log {
    public static int d(...);
    public static int v(...);
}
