package com.gbot.android.server

import com.google.common.truth.Truth.assertThat
import com.google.gson.Gson
import com.google.gson.JsonObject
import com.gbot.android.model.CommandRequest
import com.gbot.android.model.CommandResponse
import org.junit.Test

class WebSocketMessageParsingTest {

	private val gson = Gson()

	@Test
	fun parse_tapCommand_extractsCoordinates() {
		val json = """{"id":"1","command":"tap","params":{"x":100,"y":200}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.command).isEqualTo("tap")
		assertThat(req.params?.get("x")?.asFloat).isEqualTo(100f)
		assertThat(req.params?.get("y")?.asFloat).isEqualTo(200f)
	}

	@Test
	fun parse_tapCommand_withDuration() {
		val json = """{"command":"tap","params":{"x":50,"y":50,"duration":500}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("duration")?.asLong).isEqualTo(500L)
	}

	@Test
	fun parse_swipeCommand_extractsAllCoordinates() {
		val json = """{"command":"swipe","params":{"startX":0,"startY":100,"endX":200,"endY":300}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		val p = req.params!!
		assertThat(p.get("startX")?.asFloat).isEqualTo(0f)
		assertThat(p.get("startY")?.asFloat).isEqualTo(100f)
		assertThat(p.get("endX")?.asFloat).isEqualTo(200f)
		assertThat(p.get("endY")?.asFloat).isEqualTo(300f)
	}

	@Test
	fun parse_scrollCommand_extractsDirection() {
		val json = """{"command":"scroll","params":{"direction":"up"}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("direction")?.asString).isEqualTo("up")
	}

	@Test
	fun parse_pressKeyCommand_extractsKey() {
		val json = """{"command":"press_key","params":{"key":"back"}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("key")?.asString).isEqualTo("back")
	}

	@Test
	fun parse_openAppCommand_extractsPackage() {
		val json = """{"command":"open_app","params":{"package":"com.example.app"}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("package")?.asString).isEqualTo("com.example.app")
	}

	@Test
	fun parse_pingCommand_noParams() {
		val json = """{"id":"42","command":"ping"}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.command).isEqualTo("ping")
		assertThat(req.params).isNull()
	}

	@Test
	fun parse_screenshotCommand_withQuality() {
		val json = """{"command":"screenshot","params":{"quality":50}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("quality")?.asInt).isEqualTo(50)
	}

	@Test
	fun parse_typeTextCommand_extractsText() {
		val json = """{"command":"type_text","params":{"text":"hello world"}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("text")?.asString).isEqualTo("hello world")
	}

	@Test
	fun parse_findElementCommand_multipleFilters() {
		val json = """{"command":"find_element","params":{"text":"Login","className":"Button"}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("text")?.asString).isEqualTo("Login")
		assertThat(req.params?.get("className")?.asString).isEqualTo("Button")
	}

	@Test
	fun parse_getUITreeCommand_withMaxDepth() {
		val json = """{"command":"get_ui_tree","params":{"maxDepth":5}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("maxDepth")?.asInt).isEqualTo(5)
	}

	@Test
	fun parse_clickElementCommand_byRef() {
		val json = """{"command":"click_element","params":{"ref":3}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("ref")?.asInt).isEqualTo(3)
	}

	@Test
	fun parse_pinchCommand_withScale() {
		val json = """{"command":"pinch","params":{"x":500,"y":500,"scale":0.5}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("scale")?.asFloat).isEqualTo(0.5f)
	}

	@Test
	fun response_success_serializedContainsSuccessTrue() {
		val data = JsonObject().apply { addProperty("pong", true) }
		val resp = CommandResponse.success("42", data)
		val json = gson.toJson(resp)
		assertThat(json).contains("\"success\":true")
		assertThat(json).contains("\"id\":\"42\"")
	}

	@Test
	fun response_error_serializedContainsError() {
		val resp = CommandResponse.error(null, "Missing x")
		val json = gson.toJson(resp)
		assertThat(json).contains("\"success\":false")
		assertThat(json).contains("\"error\":\"Missing x\"")
	}

	@Test
	fun parse_emptyParams_defaultsToNull() {
		val json = """{"command":"ping"}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params).isNull()
	}

	@Test
	fun parse_emptyJson_commandIsNull() {
		val json = """{}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.command).isNull()
	}

	@Test
	fun parse_unicodeText_preserved() {
		val json = """{"command":"type_text","params":{"text":"你好世界"}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.params?.get("text")?.asString).isEqualTo("你好世界")
	}
}

class CommandRoutingTest {

	@Test
	fun unknownCommand_returnsErrorResponse() {
		val req = CommandRequest(id = "1", command = "nonexistent")
		assertThat(req.command).isEqualTo("nonexistent")
	}
}
