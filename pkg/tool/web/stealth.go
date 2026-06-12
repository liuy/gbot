package web

import (
	"embed"
	"fmt"
	"strings"
	"sync"
)

//go:embed stealth/*.txt
var stealthFS embed.FS

var (
	stealthScript     string
	stealthScriptOnce sync.Once
)

func buildStealthInjectionScript() string {
	stealthScriptOnce.Do(func() {
		stealthScript = buildStealthScriptFromEmbed()
	})
	return stealthScript
}

// buildStealthScriptFromEmbed builds anti-bot detection payload from embedded scripts.
// Ported from oh-my-pi's launch.ts.
func buildStealthScriptFromEmbed() string {
	entries, err := stealthFS.ReadDir("stealth")
	if err != nil {
		return ""
	}

	var scripts []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, err := stealthFS.ReadFile("stealth/" + e.Name())
		if err != nil {
			continue
		}
		scripts = append(scripts, string(data))
	}

	return buildStealthWrapper(scripts)
}

// buildStealthWrapper wraps scripts in an iframe to cache unmodified native
// functions before page scripts tamper with them.
func buildStealthWrapper(scripts []string) string {
	var joint strings.Builder
	for _, script := range scripts {
		joint.WriteString("try {\n")
		joint.WriteString(script)
		joint.WriteString("\n} catch (e) {}\n")
	}

	return fmt.Sprintf(`(() => {
	const iframe = document.createElement("iframe");
	iframe.style.display = "none";
	const container = document.head ?? document.documentElement;
	if (!container) return;
	container.appendChild(iframe);
	try {
		const nativeWindow = iframe.contentWindow;
		if (!nativeWindow) return;

		const Object_getOwnPropertyDescriptor = nativeWindow.Object.getOwnPropertyDescriptor;
		const Object_defineProperty = nativeWindow.Object.defineProperty;
		const Object_getPrototypeOf = nativeWindow.Object.getPrototypeOf;
		const Object_assign = nativeWindow.Object.assign;
		const Object_create = nativeWindow.Object.create;
		const Object_setPrototypeOf = nativeWindow.Object.setPrototypeOf;
		const Object_keys = nativeWindow.Object.keys;
		const Object_getOwnPropertyNames = nativeWindow.Object.getOwnPropertyNames;
		const Object_entries = nativeWindow.Object.entries;
		const Function_toString = nativeWindow.Function.prototype.toString;
		const Math_random = nativeWindow.Math.random;
		const Math_floor = nativeWindow.Math.floor;
		const Math_max = nativeWindow.Math.max;
		const Math_min = nativeWindow.Math.min;
		const Window_setTimeout = nativeWindow.setTimeout;
		const Window_Proxy = nativeWindow.Proxy;
		const Promise_resolve = nativeWindow.Promise.resolve.bind(nativeWindow.Promise);
		const Intl_DateTimeFormat = nativeWindow.Intl.DateTimeFormat;
		const Date_constructor = nativeWindow.Date;
		const Window_Blob = nativeWindow.Blob;

		%s
	} finally {
		if (iframe.parentNode) iframe.parentNode.removeChild(iframe);
	}
})();`, joint.String())
}
