package knobcheck

// The browser build a fingerprint name stands for in the release's core
// (ReleaseCore): sing-box-lx common/tls/utls_client.go turns the name into a
// uTLS preset, and the fork's utls u_common.go pins each *_Auto preset to one
// build. A link says "firefox"; the client sends Firefox 148's ClientHello.
// Re-read both files with a new core, as for releaseHellos.
var releaseHelloBuilds = map[string]string{
	"chrome":     "chrome_133", // HelloChrome_Auto
	"chrome_155": "chrome_155", // HelloChrome_155, the fork's opt-in preset
	"firefox":    "firefox_148",
	"edge":       "edge_85",
	"safari":     "safari_26_3",
	"ios":        "ios_14",
	"android":    "android_11_okhttp",
	"360":        "360_7_5",
	"qq":         "qq_11_1",
	"randomized": "randomized",
}

// HelloBuild is the browser build the release's core imitates for a
// fingerprint name; a name it does not know is returned as it is.
func HelloBuild(fp string) string {
	if b, ok := releaseHelloBuilds[fp]; ok {
		return b
	}
	return fp
}
