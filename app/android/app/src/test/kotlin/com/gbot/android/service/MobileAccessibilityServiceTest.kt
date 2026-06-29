package com.gbot.android.service

import android.accessibilityservice.AccessibilityService
import android.os.Build
import android.os.Looper
import com.google.common.truth.Truth.assertThat
import com.gbot.android.model.CommandRequest
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33], qualifiers = "w1080dp-h1920dp")
class MobileAccessibilityServiceTest {

	private lateinit var service: MobileAccessibilityService

	@Before
	fun setup() {
		service = Robolectric.buildService(MobileAccessibilityService::class.java).create().get()
		// ServiceController.create() does not invoke onServiceConnected; do it reflectively so
		// the companion instance/isRunning flags are set and command routing is live.
		MobileAccessibilityService::class.java.getDeclaredMethod("onServiceConnected")
			.apply { isAccessible = true }
			.invoke(service)
	}

	@After
	fun teardown() {
		service.onDestroy()
	}

	// --- Group F: lifecycle (side effects exercised by setup/teardown) ---

	@Test
	fun onServiceConnected_setsInstanceAndRunning() {
		assertThat(MobileAccessibilityService.instance).isSameInstanceAs(service)
		assertThat(MobileAccessibilityService.isRunning).isTrue()
	}

	@Test
	fun onDestroy_clearsInstanceAndRunning() {
		service.onDestroy()

		assertThat(MobileAccessibilityService.instance).isNull()
		assertThat(MobileAccessibilityService.isRunning).isFalse()
	}

	@Test
	fun onAccessibilityEvent_null_noThrow() {
		// onAccessibilityEvent is an empty no-op API contract; the meaningful assertion is completion.
		service.onAccessibilityEvent(null)
	}

	@Test
	fun onInterrupt_doesNotThrow() {
		service.onInterrupt()
	}

	// --- Group A: parameter validation ---

	@Test
	fun tap_missingX_returnsError() {
		val resp = service.handleCommand(req("tap", """{"y":10}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing x")
	}

	@Test
	fun tap_missingY_returnsError() {
		val resp = service.handleCommand(req("tap", """{"x":10}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing y")
	}

	@Test
	fun tap_nullParams_returnsError() {
		val resp = service.handleCommand(CommandRequest(null, "tap", null))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing x")
	}

	@Test
	fun longPress_missingX_returnsError() {
		val resp = service.handleCommand(req("long_press", """{"y":10}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing x")
	}

	@Test
	fun longPress_missingY_returnsError() {
		val resp = service.handleCommand(req("long_press", """{"x":10}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing y")
	}

	@Test
	fun swipe_missingStartX_returnsError() {
		val resp = service.handleCommand(req("swipe", """{"startY":0,"endX":1,"endY":1}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing startX")
	}

	@Test
	fun swipe_missingStartY_returnsError() {
		val resp = service.handleCommand(req("swipe", """{"startX":0,"endX":1,"endY":1}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing startY")
	}

	@Test
	fun swipe_missingEndX_returnsError() {
		val resp = service.handleCommand(req("swipe", """{"startX":0,"startY":0,"endY":1}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing endX")
	}

	@Test
	fun swipe_missingEndY_returnsError() {
		val resp = service.handleCommand(req("swipe", """{"startX":0,"startY":0,"endX":1}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing endY")
	}

	@Test
	fun typeText_missingText_returnsError() {
		val resp = service.handleCommand(req("type_text", "{}"))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing text")
	}

	@Test
	fun setText_missingText_returnsError() {
		val resp = service.handleCommand(req("set_text", "{}"))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing text")
	}

	@Test
	fun pressKey_missingKey_returnsError() {
		val resp = service.handleCommand(req("press_key", "{}"))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing key")
	}

	@Test
	fun pressKey_unknownKey_returnsError() {
		val resp = service.handleCommand(req("press_key", """{"key":"foobar"}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Unknown key: foobar")
	}

	@Test
	fun scroll_invalidDirection_returnsError() {
		val resp = service.handleCommand(req("scroll", """{"direction":"sideways"}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Invalid direction: sideways")
	}

	@Test
	fun openApp_missingPackage_returnsError() {
		val resp = service.handleCommand(req("open_app", "{}"))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Missing package name")
	}

	@Test
	fun openApp_notFound_returnsError() {
		val resp = service.handleCommand(req("open_app", """{"package":"does.not.exist.xyz"}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("App not found: does.not.exist.xyz")
	}

	// --- Group A2: pressKey routing (performGlobalAction returns true in Robolectric) ---

	@Test
	fun pressKey_back_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"back"}"""))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("key").asString).isEqualTo("back")
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_BACK)
	}

	@Test
	fun pressKey_home_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"home"}"""))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("key").asString).isEqualTo("home")
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_HOME)
	}

	@Test
	fun pressKey_recents_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"recents"}"""))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("key").asString).isEqualTo("recents")
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_RECENTS)
	}

	@Test
	fun pressKey_recent_alias_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"recent"}"""))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("key").asString).isEqualTo("recent")
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_RECENTS)
	}

	@Test
	fun pressKey_notifications_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"notifications"}"""))
		assertThat(resp.success).isTrue()
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_NOTIFICATIONS)
	}

	@Test
	fun pressKey_quick_settings_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"quick_settings"}"""))
		assertThat(resp.success).isTrue()
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_QUICK_SETTINGS)
	}

	@Test
	fun pressKey_power_dialog_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"power_dialog"}"""))
		assertThat(resp.success).isTrue()
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_POWER_DIALOG)
	}

	@Test
	fun pressKey_split_screen_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"split_screen"}"""))
		assertThat(resp.success).isTrue()
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_TOGGLE_SPLIT_SCREEN)
	}

	@Test
	fun pressKey_lock_screen_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"lock_screen"}"""))
		assertThat(resp.success).isTrue()
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_LOCK_SCREEN)
	}

	@Test
	fun pressKey_take_screenshot_succeeds() {
		val resp = service.handleCommand(req("press_key", """{"key":"take_screenshot"}"""))
		assertThat(resp.success).isTrue()
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_TAKE_SCREENSHOT)
	}

	@Test
	fun pressKey_caseInsensitive() {
		val resp = service.handleCommand(req("press_key", """{"key":"BACK"}"""))
		assertThat(resp.success).isTrue()
		// The echoed key preserves the original input, not the lowercased routing token.
		assertThat(resp.data!!.get("key").asString).isEqualTo("BACK")
		assertThat(shadowOf(service).globalActionsPerformed)
			.contains(AccessibilityService.GLOBAL_ACTION_BACK)
	}

	// --- Group B: framework-null error paths (rootInActiveWindow/findFocus are null) ---

	@Test
	fun getUiTree_noActiveWindow_returnsError() {
		val resp = service.handleCommand(req("get_ui_tree", "{}"))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("No active window")
	}

	@Test
	fun findElement_noActiveWindow_returnsError() {
		val resp = service.handleCommand(req("find_element", """{"text":"foo"}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("No active window")
	}

	@Test
	fun clickElement_noActiveWindow_returnsError() {
		val resp = service.handleCommand(req("click_element", """{"text":"foo"}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("No active window")
	}

	@Test
	fun typeText_noFocusedInput_returnsError() {
		val resp = service.handleCommand(req("type_text", """{"text":"hi"}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("No focused input field")
	}

	@Test
	fun setText_noFocusedInput_returnsError() {
		val resp = service.handleCommand(req("set_text", """{"text":"hi"}"""))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("No focused input field")
	}

	@Test
	fun getFocused_none_returnsFocusedFalse() {
		val resp = service.handleCommand(CommandRequest(null, "get_focused", null))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("focused").asBoolean).isFalse()
	}

	@Test
	fun notifications_noActiveWindow_returnsSuccessEmpty() {
		val resp = service.handleCommand(CommandRequest(null, "notifications", null))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.getAsJsonArray("notifications").size()).isEqualTo(0)
	}

	@Test
	fun screenshot_failure_returnsError() {
		shadowOf(service).setTakeScreenshotErrorCode(-1)

		val resp = runWithMainLooperPumped(CommandRequest(null, "screenshot", null))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Screenshot failed")
	}

	// The screenshot SUCCESS path (onSuccess → wrapHardwareBuffer → compress → base64) is
	// unreachable in Robolectric: ShadowAccessibilityService's 1×1 HardwareBuffer lacks
	// USAGE_GPU_SAMPLED_IMAGE, so Bitmap.wrapHardwareBuffer throws IllegalArgumentException
	// before the compress path runs. Covering this requires a real device or mocking the
	// HardwareBuffer usage flags — out of scope for unit tests.

	// --- Group D: routing/dispatch ---

	@Test
	fun ping_returnsPongTrue() {
		val resp = service.handleCommand(CommandRequest(null, "ping", null))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("pong").asBoolean).isTrue()
	}

	@Test
	fun getDeviceInfo_returnsBuildAndDisplayFields() {
		val resp = service.handleCommand(CommandRequest(null, "get_device_info", null))
		assertThat(resp.success).isTrue()
		val data = resp.data!!
		assertThat(data.get("manufacturer").asString).isNotEmpty()
		assertThat(data.get("model").asString).isNotEmpty()
		assertThat(data.get("sdk").asInt).isEqualTo(Build.VERSION.SDK_INT)
		assertThat(data.get("release").asString).isNotEmpty()
		assertThat(data.get("screenWidth").asInt).isNotEqualTo(0)
		assertThat(data.get("screenHeight").asInt).isNotEqualTo(0)
		assertThat(data.get("density").asFloat).isGreaterThan(0f)
		assertThat(data.get("densityDpi").asInt).isGreaterThan(0)
	}

	@Test
	fun unknownCommand_returnsError() {
		val resp = service.handleCommand(CommandRequest(null, "frobnicate", null))
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Unknown command: frobnicate")
	}

	@Test
	fun allKnownCommands_routeWithoutUnknownCommandError() {
		val commands = listOf(
			"tap", "long_press", "swipe", "type_text", "set_text",
			"press_key", "get_ui_tree", "find_element", "screenshot",
			"wait_for_element", "open_app", "get_device_info", "scroll",
			"pinch", "click_element", "get_focused", "notifications", "ping"
		)
		for (cmd in commands) {
			val resp = service.handleCommand(CommandRequest(null, cmd, null))
			// Every known command must route to a handler, not the "Unknown command" branch.
			// Params may be missing → error responses are fine, but the error must NOT be
			// "Unknown command: <cmd>".
			if (!resp.success) {
				assertThat(resp.error).doesNotContain("Unknown command")
			}
		}
	}

	// --- Group E: wait_for_element poll loop ---

	@Test
	fun waitForElement_notFound_returnsFoundFalseWithinTimeout() {
		val resp = service.handleCommand(req("wait_for_element", """{"text":"missing","timeout":200}"""))
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("found").asBoolean).isFalse()
		assertThat(resp.data!!.get("elapsed_ms").asLong).isEqualTo(200L)
	}

	// --- Group C: gestures (setCanDispatchGestures(true) + reflective callback; no 5s timeouts) ---

	private fun fireGestureCompleted() {
		shadowOf(service).gesturesDispatched.last().callback().onCompleted(null)
	}

	private fun fireGestureCancelled() {
		shadowOf(service).gesturesDispatched.last().callback().onCancelled(null)
	}

	// handleCommand runs dispatchGesture (shadow records the callback synchronously) then blocks on a
	// 5s latch. To let the latch count down we fire the recorded callback from this thread, but
	// dispatch must happen first, so handleCommand runs on a worker. Bounded waits keep the suite
	// fast and leak no lingering threads.
	private fun runGesture(request: CommandRequest, fire: () -> Unit): com.gbot.android.model.CommandResponse {
		shadowOf(service).setCanDispatchGestures(true)
		var resp: com.gbot.android.model.CommandResponse? = null
		val done = java.util.concurrent.CountDownLatch(1)
		var threadError: Throwable? = null
		val t = Thread {
			try {
				resp = service.handleCommand(request)
			} catch (e: Throwable) {
				threadError = e
			}
			done.countDown()
		}.apply { isDaemon = true }
		t.start()
		// Wait for the shadow to record the dispatch (dispatchGesture runs before the latch await).
		// Pump the main looper: handlers that run on the main thread (e.g. resources/display queries
		// issued by scroll/pinch) are dispatched by the paused looper, so idling unblocks the worker.
		val mainShadow = shadowOf(Looper.getMainLooper())
		val waitStart = System.currentTimeMillis()
		while (shadowOf(service).gesturesDispatched.isEmpty() && System.currentTimeMillis() - waitStart < 2000) {
			mainShadow.idle()
			Thread.yield()
			Thread.sleep(1)
		}
		threadError?.let { throw it }
		check(shadowOf(service).gesturesDispatched.isNotEmpty()) {
			"no gesture dispatched after 5s; threadError=$threadError resp=$resp"
		}
		fire()
		val completed = done.await(3, java.util.concurrent.TimeUnit.SECONDS)
		assertThat(completed).isTrue()
		return resp!!
	}

	@Test
	fun tap_validParams_succeeds() {
		val resp = runGesture(req("tap", """{"x":100,"y":200,"duration":100}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("tap(100.0, 200.0)")
	}

	@Test
	fun longPress_validParams_succeeds() {
		val resp = runGesture(req("long_press", """{"x":100,"y":200}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("longPress(100.0, 200.0)")
	}

	@Test
	fun swipe_validParams_succeeds() {
		val resp = runGesture(req("swipe", """{"startX":0,"startY":0,"endX":100,"endY":100}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("swipe(0.0,0.0 -> 100.0,100.0)")
	}

	@Test
	fun scroll_down_succeeds() {
		val resp = runGesture(req("scroll", """{"direction":"down"}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("scroll(down)")
	}

	@Test
	fun scroll_up_succeeds() {
		val resp = runGesture(req("scroll", """{"direction":"up"}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("scroll(up)")
	}

	@Test
	fun scroll_left_succeeds() {
		val resp = runGesture(req("scroll", """{"direction":"left"}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("scroll(left)")
	}

	@Test
	fun scroll_right_succeeds() {
		val resp = runGesture(req("scroll", """{"direction":"right"}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("scroll(right)")
	}

	@Test
	fun pinch_validParams_succeeds() {
		// Center must be >= startDist*scale so the two strokes stay in non-negative bounds
		// (StrokeDescription rejects negative path coordinates): startDist=100, scale=1.5 -> endDist=150.
		val resp = runGesture(req("pinch", """{"x":200,"y":200}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("pinch(scale=1.5)")
	}

	@Test
	fun pinch_customScale_succeeds() {
		val resp = runGesture(req("pinch", """{"scale":0.5}""")) { fireGestureCompleted() }
		assertThat(resp.success).isTrue()
		assertThat(resp.data!!.get("action").asString).isEqualTo("pinch(scale=0.5)")
	}

	@Test
	fun tap_cancelled_returnsError() {
		val resp = runGesture(req("tap", """{"x":100,"y":200}""")) { fireGestureCancelled() }
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("Gesture tap(100.0, 200.0) failed or timed out")
	}

	// Runs handleCommand on a worker thread while pumping the main looper. Needed for screenshot:
	// the service blocks on a latch while takeScreenshot's callback is queued on the main executor,
	// so the main thread must idle the looper for the callback to fire and the latch to count down.
	private fun runWithMainLooperPumped(request: CommandRequest): com.gbot.android.model.CommandResponse {
		lateinit var resp: com.gbot.android.model.CommandResponse
		val t = Thread { resp = service.handleCommand(request) }
		t.start()
		while (t.isAlive) {
			shadowOf(Looper.getMainLooper()).idle()
			Thread.sleep(2)
		}
		t.join(2000)
		return resp
	}

	private fun req(command: String, paramsJson: String): CommandRequest {
		val params = com.google.gson.JsonParser.parseString(paramsJson).asJsonObject
		return CommandRequest(null, command, params)
	}
}
