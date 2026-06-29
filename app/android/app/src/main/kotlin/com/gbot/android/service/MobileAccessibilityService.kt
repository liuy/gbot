package com.gbot.android.service

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Path
import android.os.Build
import android.util.Base64
import android.util.Log
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import com.google.gson.Gson
import com.google.gson.JsonObject
import com.gbot.android.model.CommandRequest
import com.gbot.android.model.CommandResponse
import com.gbot.android.model.UINode
import com.gbot.android.server.WebSocketCommandServer
import kotlinx.coroutines.*
import java.io.ByteArrayOutputStream
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

class MobileAccessibilityService : AccessibilityService() {

    companion object {
        private const val TAG = "MobileAccessibility"
        var instance: MobileAccessibilityService? = null
            private set
        var isRunning = false
            private set
    }

    private val gson = Gson()
    private val serviceScope = CoroutineScope(Dispatchers.Main + SupervisorJob())

    override fun onServiceConnected() {
        super.onServiceConnected()
        instance = this
        isRunning = true
        Log.i(TAG, "Accessibility service connected")
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        // Events are not actively processed; UI tree is fetched on demand
    }

    override fun onInterrupt() {
        Log.w(TAG, "Accessibility service interrupted")
    }

    override fun onDestroy() {
        super.onDestroy()
        instance = null
        isRunning = false
        serviceScope.cancel()
        Log.i(TAG, "Accessibility service destroyed")
    }

    fun handleCommand(request: CommandRequest): CommandResponse {
        return try {
            when (request.command) {
                "tap" -> executeTap(request.params)
                "long_press" -> executeLongPress(request.params)
                "swipe" -> executeSwipe(request.params)
                "type_text" -> executeTypeText(request.params)
                "set_text" -> executeSetText(request.params)
                "press_key" -> executePressKey(request.params)
                "get_ui_tree" -> executeGetUITree(request.params)
                "find_element" -> executeFindElement(request.params)
                "screenshot" -> executeScreenshot(request.params)
                "wait_for_element" -> executeWaitForElement(request.params)
                "open_app" -> executeOpenApp(request.params)
                "get_device_info" -> executeGetDeviceInfo()
                "scroll" -> executeScroll(request.params)
                "pinch" -> executePinch(request.params)
                "click_element" -> executeClickElement(request.params)
                "get_focused" -> executeGetFocused()
                "notifications" -> executeGetNotifications()
                "ping" -> CommandResponse.success(request.id, JsonObject().apply { addProperty("pong", true) })
                else -> CommandResponse.error(request.id, "Unknown command: ${request.command}")
            }
        } catch (e: Exception) {
            Log.e(TAG, "Error handling command ${request.command}", e)
            CommandResponse.error(request.id, "Error: ${e.message}")
        }
    }

    // --- Gesture Commands ---

    private fun executeTap(params: JsonObject?): CommandResponse {
        val x = params?.get("x")?.asFloat ?: return CommandResponse.error(null, "Missing x")
        val y = params.get("y")?.asFloat ?: return CommandResponse.error(null, "Missing y")
        val duration = params.get("duration")?.asLong ?: 100L

        val path = Path().apply { moveTo(x, y) }
        val stroke = GestureDescription.StrokeDescription(path, 0, duration)
        val gesture = GestureDescription.Builder().addStroke(stroke).build()

        return dispatchGestureSync(gesture, "tap($x, $y)")
    }

    private fun executeLongPress(params: JsonObject?): CommandResponse {
        val x = params?.get("x")?.asFloat ?: return CommandResponse.error(null, "Missing x")
        val y = params.get("y")?.asFloat ?: return CommandResponse.error(null, "Missing y")
        val duration = params.get("duration")?.asLong ?: 1000L

        val path = Path().apply { moveTo(x, y) }
        val stroke = GestureDescription.StrokeDescription(path, 0, duration)
        val gesture = GestureDescription.Builder().addStroke(stroke).build()

        return dispatchGestureSync(gesture, "longPress($x, $y)")
    }

    private fun executeSwipe(params: JsonObject?): CommandResponse {
        val startX = params?.get("startX")?.asFloat ?: return CommandResponse.error(null, "Missing startX")
        val startY = params.get("startY")?.asFloat ?: return CommandResponse.error(null, "Missing startY")
        val endX = params.get("endX")?.asFloat ?: return CommandResponse.error(null, "Missing endX")
        val endY = params.get("endY")?.asFloat ?: return CommandResponse.error(null, "Missing endY")
        val duration = params.get("duration")?.asLong ?: 300L

        val path = Path().apply {
            moveTo(startX, startY)
            lineTo(endX, endY)
        }
        val stroke = GestureDescription.StrokeDescription(path, 0, duration)
        val gesture = GestureDescription.Builder().addStroke(stroke).build()

        return dispatchGestureSync(gesture, "swipe($startX,$startY -> $endX,$endY)")
    }

    private fun executeScroll(params: JsonObject?): CommandResponse {
        val direction = params?.get("direction")?.asString ?: "down"
        val amount = params?.get("amount")?.asFloat ?: 500f

        val displayMetrics = resources.displayMetrics
        val centerX = displayMetrics.widthPixels / 2f
        val centerY = displayMetrics.heightPixels / 2f

        val (startX, startY, endX, endY) = when (direction) {
            "up" -> listOf(centerX, centerY - amount / 2, centerX, centerY + amount / 2)
            "down" -> listOf(centerX, centerY + amount / 2, centerX, centerY - amount / 2)
            "left" -> listOf(centerX - amount / 2, centerY, centerX + amount / 2, centerY)
            "right" -> listOf(centerX + amount / 2, centerY, centerX - amount / 2, centerY)
            else -> return CommandResponse.error(null, "Invalid direction: $direction")
        }

        val path = Path().apply {
            moveTo(startX, startY)
            lineTo(endX, endY)
        }
        val stroke = GestureDescription.StrokeDescription(path, 0, 400)
        val gesture = GestureDescription.Builder().addStroke(stroke).build()

        return dispatchGestureSync(gesture, "scroll($direction)")
    }

    private fun executePinch(params: JsonObject?): CommandResponse {
        val centerX = params?.get("x")?.asFloat ?: (resources.displayMetrics.widthPixels / 2f)
        val centerY = params?.get("y")?.asFloat ?: (resources.displayMetrics.heightPixels / 2f)
        val scale = params?.get("scale")?.asFloat ?: 1.5f // >1 = zoom in, <1 = zoom out
        val duration = params?.get("duration")?.asLong ?: 400L

        val startDist = 100f
        val endDist = startDist * scale

        val path1 = Path().apply {
            moveTo(centerX - startDist, centerY)
            lineTo(centerX - endDist, centerY)
        }
        val path2 = Path().apply {
            moveTo(centerX + startDist, centerY)
            lineTo(centerX + endDist, centerY)
        }

        val gesture = GestureDescription.Builder()
            .addStroke(GestureDescription.StrokeDescription(path1, 0, duration))
            .addStroke(GestureDescription.StrokeDescription(path2, 0, duration))
            .build()

        return dispatchGestureSync(gesture, "pinch(scale=$scale)")
    }

    private fun dispatchGestureSync(gesture: GestureDescription, label: String): CommandResponse {
        val latch = CountDownLatch(1)
        var success = false

        dispatchGesture(gesture, object : GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) {
                success = true
                latch.countDown()
            }

            override fun onCancelled(gestureDescription: GestureDescription?) {
                success = false
                latch.countDown()
            }
        }, null)

        latch.await(5, TimeUnit.SECONDS)
        return if (success) {
            CommandResponse.success(null, JsonObject().apply { addProperty("action", label) })
        } else {
            CommandResponse.error(null, "Gesture $label failed or timed out")
        }
    }

    // --- Text Commands ---

    private fun executeTypeText(params: JsonObject?): CommandResponse {
        val text = params?.get("text")?.asString ?: return CommandResponse.error(null, "Missing text")
        val focused = findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            ?: return CommandResponse.error(null, "No focused input field")

        val args = android.os.Bundle().apply {
            putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,
                (focused.text?.toString() ?: "") + text)
        }
        val result = focused.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)
        focused.recycle()

        return if (result) {
            CommandResponse.success(null, JsonObject().apply { addProperty("typed", text) })
        } else {
            CommandResponse.error(null, "Failed to type text")
        }
    }

    private fun executeSetText(params: JsonObject?): CommandResponse {
        val text = params?.get("text")?.asString ?: return CommandResponse.error(null, "Missing text")
        val focused = findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            ?: return CommandResponse.error(null, "No focused input field")

        val args = android.os.Bundle().apply {
            putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, text)
        }
        val result = focused.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)
        focused.recycle()

        return if (result) {
            CommandResponse.success(null, JsonObject().apply { addProperty("text_set", text) })
        } else {
            CommandResponse.error(null, "Failed to set text")
        }
    }

    // --- Key Commands ---

    private fun executePressKey(params: JsonObject?): CommandResponse {
        val key = params?.get("key")?.asString ?: return CommandResponse.error(null, "Missing key")
        val action = when (key.lowercase()) {
            "back" -> GLOBAL_ACTION_BACK
            "home" -> GLOBAL_ACTION_HOME
            "recents", "recent" -> GLOBAL_ACTION_RECENTS
            "notifications" -> GLOBAL_ACTION_NOTIFICATIONS
            "quick_settings" -> GLOBAL_ACTION_QUICK_SETTINGS
            "power_dialog" -> GLOBAL_ACTION_POWER_DIALOG
            "split_screen" -> GLOBAL_ACTION_TOGGLE_SPLIT_SCREEN
            "lock_screen" -> GLOBAL_ACTION_LOCK_SCREEN
            "take_screenshot" -> GLOBAL_ACTION_TAKE_SCREENSHOT
            else -> return CommandResponse.error(null, "Unknown key: $key")
        }

        val result = performGlobalAction(action)
        return if (result) {
            CommandResponse.success(null, JsonObject().apply { addProperty("key", key) })
        } else {
            CommandResponse.error(null, "Failed to press key: $key")
        }
    }

    // --- UI Tree Commands ---

    private fun executeGetUITree(params: JsonObject?): CommandResponse {
        val maxDepth = params?.get("maxDepth")?.asInt ?: 15
        val root = rootInActiveWindow ?: return CommandResponse.error(null, "No active window")

        val tree = buildUITree(root, 0, maxDepth)
        root.recycle()

        val json = gson.toJsonTree(tree)
        return CommandResponse.success(null, JsonObject().apply { add("tree", json) })
    }

    private fun executeFindElement(params: JsonObject?): CommandResponse {
        val root = rootInActiveWindow ?: return CommandResponse.error(null, "No active window")

        val results = mutableListOf<UINode>()
        val text = params?.get("text")?.asString
        val id = params?.get("id")?.asString
        val className = params?.get("className")?.asString
        val contentDesc = params?.get("contentDescription")?.asString
        val maxResults = params?.get("maxResults")?.asInt ?: 10

        findElements(root, text, id, className, contentDesc, results, maxResults)
        root.recycle()

        val json = gson.toJsonTree(results)
        return CommandResponse.success(null, JsonObject().apply {
            add("elements", json)
            addProperty("count", results.size)
        })
    }

    private fun executeClickElement(params: JsonObject?): CommandResponse {
        val root = rootInActiveWindow ?: return CommandResponse.error(null, "No active window")

        val text = params?.get("text")?.asString
        val id = params?.get("id")?.asString
        val contentDesc = params?.get("contentDescription")?.asString

        val node = findFirstElement(root, text, id, null, contentDesc)
        root.recycle()

        if (node == null) return CommandResponse.error(null, "Element not found")

        // Walk up to find clickable ancestor if this node isn't clickable
        var target = node
        while (target != null && !target.isClickable) {
            val parent = target.parent
            if (target != node) target.recycle()
            target = parent
        }

        if (target == null) {
            node.recycle()
            return CommandResponse.error(null, "No clickable element found")
        }

        val result = target.performAction(AccessibilityNodeInfo.ACTION_CLICK)
        val desc = node.text?.toString() ?: node.contentDescription?.toString() ?: "element"
        if (target != node) target.recycle()
        node.recycle()

        return if (result) {
            CommandResponse.success(null, JsonObject().apply { addProperty("clicked", desc) })
        } else {
            CommandResponse.error(null, "Click action failed")
        }
    }

    private fun executeWaitForElement(params: JsonObject?): CommandResponse {
        val text = params?.get("text")?.asString
        val id = params?.get("id")?.asString
        val className = params?.get("className")?.asString
        val contentDesc = params?.get("contentDescription")?.asString
        val timeoutMs = params?.get("timeout")?.asLong ?: 10000L
        val pollIntervalMs = 500L

        val startTime = System.currentTimeMillis()
        while (System.currentTimeMillis() - startTime < timeoutMs) {
            val root = rootInActiveWindow
            if (root != null) {
                val node = findFirstElement(root, text, id, className, contentDesc)
                if (node != null) {
                    val uiNode = nodeToUINode(node)
                    node.recycle()
                    root.recycle()
                    return CommandResponse.success(null, JsonObject().apply {
                        add("element", gson.toJsonTree(uiNode))
                        addProperty("found", true)
                        addProperty("elapsed_ms", System.currentTimeMillis() - startTime)
                    })
                }
                root.recycle()
            }
            Thread.sleep(pollIntervalMs)
        }

        return CommandResponse.success(null, JsonObject().apply {
            addProperty("found", false)
            addProperty("elapsed_ms", timeoutMs)
        })
    }

    private fun executeGetFocused(): CommandResponse {
        val focused = findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
        if (focused == null) {
            return CommandResponse.success(null, JsonObject().apply { addProperty("focused", false) })
        }
        val uiNode = nodeToUINode(focused)
        focused.recycle()
        return CommandResponse.success(null, JsonObject().apply {
            addProperty("focused", true)
            add("element", gson.toJsonTree(uiNode))
        })
    }

    private fun executeGetNotifications(): CommandResponse {
        // Use the accessibility events - we have access through service info
        val root = rootInActiveWindow
        // Trigger notifications panel
        performGlobalAction(GLOBAL_ACTION_NOTIFICATIONS)
        Thread.sleep(500)

        val notifRoot = rootInActiveWindow
        val results = mutableListOf<UINode>()
        if (notifRoot != null) {
            findElements(notifRoot, null, null, "android.widget.FrameLayout", null, results, 50)
            notifRoot.recycle()
        }

        performGlobalAction(GLOBAL_ACTION_BACK)

        val json = gson.toJsonTree(results)
        return CommandResponse.success(null, JsonObject().apply {
            add("notifications", json)
        })
    }

    // --- Screenshot ---

    private fun executeScreenshot(params: JsonObject?): CommandResponse {
        val quality = params?.get("quality")?.asInt ?: 80
        val latch = CountDownLatch(1)
        var resultBitmap: Bitmap? = null

        takeScreenshot(android.view.Display.DEFAULT_DISPLAY,
            mainExecutor,
            object : TakeScreenshotCallback {
                override fun onSuccess(screenshot: ScreenshotResult) {
                    val hwBitmap = Bitmap.wrapHardwareBuffer(
                        screenshot.hardwareBuffer, screenshot.colorSpace
                    )
                    resultBitmap = hwBitmap?.copy(Bitmap.Config.ARGB_8888, false)
                    hwBitmap?.recycle()
                    screenshot.hardwareBuffer.close()
                    latch.countDown()
                }

                override fun onFailure(errorCode: Int) {
                    Log.e(TAG, "Screenshot failed with code: $errorCode")
                    latch.countDown()
                }
            })

        latch.await(10, TimeUnit.SECONDS)

        val bitmap = resultBitmap ?: return CommandResponse.error(null, "Screenshot failed")
        val baos = ByteArrayOutputStream()
        bitmap.compress(Bitmap.CompressFormat.JPEG, quality, baos)
        val base64 = Base64.encodeToString(baos.toByteArray(), Base64.NO_WRAP)
        val width = bitmap.width
        val height = bitmap.height
        bitmap.recycle()

        return CommandResponse.success(null, JsonObject().apply {
            addProperty("image", base64)
            addProperty("format", "jpeg")
            addProperty("width", width)
            addProperty("height", height)
        })
    }

    // --- App Commands ---

    private fun executeOpenApp(params: JsonObject?): CommandResponse {
        val packageName = params?.get("package")?.asString
            ?: return CommandResponse.error(null, "Missing package name")

        val intent = packageManager.getLaunchIntentForPackage(packageName)
            ?: return CommandResponse.error(null, "App not found: $packageName")

        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        startActivity(intent)

        return CommandResponse.success(null, JsonObject().apply {
            addProperty("opened", packageName)
        })
    }

    private fun executeGetDeviceInfo(): CommandResponse {
        val displayMetrics = resources.displayMetrics
        return CommandResponse.success(null, JsonObject().apply {
            addProperty("manufacturer", Build.MANUFACTURER)
            addProperty("model", Build.MODEL)
            addProperty("sdk", Build.VERSION.SDK_INT)
            addProperty("release", Build.VERSION.RELEASE)
            addProperty("screenWidth", displayMetrics.widthPixels)
            addProperty("screenHeight", displayMetrics.heightPixels)
            addProperty("density", displayMetrics.density)
            addProperty("densityDpi", displayMetrics.densityDpi)
        })
    }

    // --- UI Tree Helpers ---

    private fun buildUITree(node: AccessibilityNodeInfo, depth: Int, maxDepth: Int): UINode {
        val uiNode = nodeToUINode(node)

        if (depth < maxDepth) {
            val children = mutableListOf<UINode>()
            for (i in 0 until node.childCount) {
                val child = node.getChild(i) ?: continue
                children.add(buildUITree(child, depth + 1, maxDepth))
                child.recycle()
            }
            if (children.isNotEmpty()) {
                uiNode.children = children
            }
        }
        return uiNode
    }

    private fun nodeToUINode(node: AccessibilityNodeInfo): UINode {
        val rect = android.graphics.Rect()
        node.getBoundsInScreen(rect)

        return UINode(
            className = node.className?.toString(),
            text = node.text?.toString(),
            contentDescription = node.contentDescription?.toString(),
            viewId = node.viewIdResourceName,
            isClickable = node.isClickable,
            isScrollable = node.isScrollable,
            isEditable = node.isEditable,
            isEnabled = node.isEnabled,
            isChecked = node.isChecked,
            isFocused = node.isFocused,
            isSelected = node.isSelected,
            bounds = mapOf(
                "left" to rect.left,
                "top" to rect.top,
                "right" to rect.right,
                "bottom" to rect.bottom
            ),
            packageName = node.packageName?.toString()
        )
    }

    private fun findElements(
        node: AccessibilityNodeInfo,
        text: String?,
        id: String?,
        className: String?,
        contentDesc: String?,
        results: MutableList<UINode>,
        maxResults: Int
    ) {
        if (results.size >= maxResults) return

        if (matchesFilter(node, text, id, className, contentDesc)) {
            results.add(nodeToUINode(node))
        }

        for (i in 0 until node.childCount) {
            val child = node.getChild(i) ?: continue
            findElements(child, text, id, className, contentDesc, results, maxResults)
            child.recycle()
        }
    }

    private fun findFirstElement(
        node: AccessibilityNodeInfo,
        text: String?,
        id: String?,
        className: String?,
        contentDesc: String?
    ): AccessibilityNodeInfo? {
        if (matchesFilter(node, text, id, className, contentDesc)) {
            return AccessibilityNodeInfo.obtain(node)
        }

        for (i in 0 until node.childCount) {
            val child = node.getChild(i) ?: continue
            val found = findFirstElement(child, text, id, className, contentDesc)
            if (found != null) {
                child.recycle()
                return found
            }
            child.recycle()
        }
        return null
    }

    private fun matchesFilter(
        node: AccessibilityNodeInfo,
        text: String?,
        id: String?,
        className: String?,
        contentDesc: String?
    ): Boolean {
        if (text != null) {
            val nodeText = node.text?.toString() ?: ""
            val nodeDesc = node.contentDescription?.toString() ?: ""
            if (!nodeText.contains(text, ignoreCase = true) &&
                !nodeDesc.contains(text, ignoreCase = true)) return false
        }
        if (id != null && node.viewIdResourceName?.contains(id, ignoreCase = true) != true) return false
        if (className != null && node.className?.toString()?.contains(className, ignoreCase = true) != true) return false
        if (contentDesc != null && node.contentDescription?.toString()?.contains(contentDesc, ignoreCase = true) != true) return false
        return true
    }
}
