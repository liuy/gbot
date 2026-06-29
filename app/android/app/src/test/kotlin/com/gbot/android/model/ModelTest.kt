package com.gbot.android.model

import com.google.common.truth.Truth.assertThat
import com.google.gson.Gson
import com.google.gson.JsonObject
import org.junit.Test

class CommandResponseTest {

	private val gson = Gson()

	@Test
	fun success_setsAllFields() {
		val data = JsonObject().apply { addProperty("pong", true) }
		val resp = CommandResponse.success("req-1", data)
		assertThat(resp.id).isEqualTo("req-1")
		assertThat(resp.success).isTrue()
		assertThat(resp.data?.get("pong")?.asBoolean).isTrue()
		assertThat(resp.error).isNull()
	}

	@Test
	fun success_withNullId_preservesNull() {
		val data = JsonObject().apply { addProperty("ok", 1) }
		val resp = CommandResponse.success(null, data)
		assertThat(resp.id).isNull()
		assertThat(resp.success).isTrue()
	}

	@Test
	fun error_setsErrorFields() {
		val resp = CommandResponse.error("req-2", "missing x")
		assertThat(resp.id).isEqualTo("req-2")
		assertThat(resp.success).isFalse()
		assertThat(resp.data).isNull()
		assertThat(resp.error).isEqualTo("missing x")
	}

	@Test
	fun error_withNullId_preservesNull() {
		val resp = CommandResponse.error(null, "bad json")
		assertThat(resp.id).isNull()
		assertThat(resp.success).isFalse()
		assertThat(resp.error).isEqualTo("bad json")
	}

	@Test
	fun serialization_roundTrip_preservesAllFields() {
		val data = JsonObject().apply {
			addProperty("action", "tap")
			addProperty("x", 100f)
		}
		val original = CommandResponse.success("req-3", data)
		val json = gson.toJson(original)
		val restored = gson.fromJson(json, CommandResponse::class.java)
		assertThat(restored.id).isEqualTo("req-3")
		assertThat(restored.success).isTrue()
		assertThat(restored.data?.get("action")?.asString).isEqualTo("tap")
	}

	@Test
	fun serialization_errorResponse_preservesError() {
		val original = CommandResponse.error("req-4", "timeout")
		val json = gson.toJson(original)
		val restored = gson.fromJson(json, CommandResponse::class.java)
		assertThat(restored.success).isFalse()
		assertThat(restored.error).isEqualTo("timeout")
	}
}

class CommandRequestTest {

	private val gson = Gson()

	@Test
	fun parse_fullCommandWithParams() {
		val json = """{"id":"abc","command":"tap","params":{"x":10,"y":20}}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.id).isEqualTo("abc")
		assertThat(req.command).isEqualTo("tap")
		assertThat(req.params?.get("x")?.asInt).isEqualTo(10)
		assertThat(req.params?.get("y")?.asInt).isEqualTo(20)
	}

	@Test
	fun parse_commandOnly_nullIdAndParams() {
		val json = """{"command":"ping"}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.id).isNull()
		assertThat(req.command).isEqualTo("ping")
		assertThat(req.params).isNull()
	}

	@Test
	fun parse_nullCommandField_setsNull() {
		val json = """{"command":null}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.command).isNull()
	}

	@Test
	fun parse_missingCommandField_setsNull() {
		val json = """{"id":"x"}"""
		val req = gson.fromJson(json, CommandRequest::class.java)
		assertThat(req.command).isNull()
	}
}

class UINodeTest {

	@Test
	fun defaultValues_areCorrect() {
		val node = UINode()
		assertThat(node.className).isNull()
		assertThat(node.text).isNull()
		assertThat(node.isClickable).isFalse()
		assertThat(node.isScrollable).isFalse()
		assertThat(node.isEditable).isFalse()
		assertThat(node.isEnabled).isTrue()
		assertThat(node.isChecked).isFalse()
		assertThat(node.children).isNull()
	}

	@Test
	fun bounds_areMutableMap() {
		val bounds = mapOf("left" to 0, "top" to 10, "right" to 100, "bottom" to 200)
		val node = UINode(text = "btn", bounds = bounds)
		assertThat(node.bounds?.get("left")).isEqualTo(0)
		assertThat(node.bounds?.get("bottom")).isEqualTo(200)
	}

	@Test
	fun serialization_roundTrip_preservesChildren() {
		val child = UINode(text = "child", isClickable = true)
		val parent = UINode(text = "parent", children = listOf(child))
		val gson = Gson()
		val json = gson.toJson(parent)
		val restored = gson.fromJson(json, UINode::class.java)
		assertThat(restored.text).isEqualTo("parent")
		assertThat(restored.children).hasSize(1)
		assertThat(restored.children!![0].text).isEqualTo("child")
		assertThat(restored.children!![0].isClickable).isTrue()
	}

	@Test
	fun serialization_nullChildren_omitted() {
		val node = UINode(text = "leaf")
		val gson = Gson()
		val json = gson.toJson(node)
		assertThat(json).doesNotContain("children")
	}
}
