package computer

import "encoding/json"

// inputSchema returns the JSON Schema for the Computer tool input. Only
// `action` is required; per-action parameter requirements (host for connect,
// coordinate for click, etc.) are validated in dispatch, not by the schema —
// JSON Schema's conditional `required` is poorly supported across providers.
func inputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["action"],
  "properties": {
    "action": {
      "type": "string",
      "enum": ["connect", "disconnect", "screen", "screenshot", "click", "click_element", "open_menu", "open_menu_element", "type", "send_key", "scroll", "zoom", "device_info", "open_app"],
      "description": "Computer-use action. connect: open a WebSocket to a GBot Android app (params: host, port?, password?). disconnect: close it. screen: list on-screen elements with refs (token-cheap). screenshot: capture a JPEG. click/click_element: tap a coordinate or ref. open_menu/open_menu_element: long-press a coordinate or ref. type: replace focused field text. send_key: press a system key (back, home, recents, notifications, quick_settings, power_dialog, split_screen, lock_screen, take_screenshot). scroll: direction up|down|left|right. zoom: pinch at coordinate. device_info: report manufacturer/model/screen. open_app: launch an installed app by package name."
    },
    "host": {
      "type": "string",
      "description": "[connect] Android device address, e.g. 192.168.1.100."
    },
    "port": {
      "type": "integer",
      "description": "[connect] GBot WebSocket port. Defaults to 8765 when omitted."
    },
    "password": {
      "type": "string",
      "description": "[connect] optional GBot bearer token."
    },
    "max_depth": {
      "type": "integer",
      "description": "[screen] max UI tree depth. Defaults to 15; no upper clamp."
    },
    "ref": {
      "type": "integer",
      "description": "[click_element, open_menu_element] element ref from the most recent screen call."
    },
    "coordinate": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 2,
      "maxItems": 2,
      "description": "[click, open_menu, zoom] [x, y] screen pixel coordinate."
    },
    "direction": {
      "type": "string",
      "enum": ["up", "down", "left", "right"],
      "description": "[scroll] direction."
    },
    "text": {
      "type": "string",
      "description": "[type] text to type into the focused field (non-empty)."
    },
    "mode": {
      "type": "string",
      "enum": ["replace", "append"],
      "description": "[type] replace (default) overwrites the field; append adds to existing text. Use append for custom Views that reject set_text."
    },
    "key": {
      "type": "string",
      "description": "[send_key] one of: back, home, recents, notifications, quick_settings, power_dialog, split_screen, lock_screen, take_screenshot."
    },
    "scale": {
      "type": "number",
      "description": "[zoom] pinch scale factor (>1 zoom in, <1 zoom out)."
    },
    "package": {
      "type": "string",
      "description": "[open_app] app package name, e.g. com.android.chrome."
    }
  }
}`)
}
