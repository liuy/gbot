package com.gbot.android.model

import com.google.gson.JsonObject

data class CommandRequest(
    val id: String?,
    val command: String,
    val params: JsonObject? = null
)

data class CommandResponse(
    val id: String?,
    val success: Boolean,
    val data: JsonObject? = null,
    val error: String? = null
) {
    companion object {
        fun success(id: String?, data: JsonObject): CommandResponse {
            return CommandResponse(id = id, success = true, data = data)
        }

        fun error(id: String?, message: String): CommandResponse {
            return CommandResponse(id = id, success = false, error = message)
        }
    }
}

data class UINode(
    val className: String? = null,
    val text: String? = null,
    val contentDescription: String? = null,
    val viewId: String? = null,
    val isClickable: Boolean = false,
    val isScrollable: Boolean = false,
    val isEditable: Boolean = false,
    val isEnabled: Boolean = true,
    val isChecked: Boolean = false,
    val isFocused: Boolean = false,
    val isSelected: Boolean = false,
    val bounds: Map<String, Int>? = null,
    val packageName: String? = null,
    var children: List<UINode>? = null
)
