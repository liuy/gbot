package com.gbot.android

import com.google.common.truth.Truth.assertThat
import org.junit.Test

/**
 * Pure logic for the MULTI-ENDPOINT WUI target switch: a JSON-array list of
 * named remote daemons in SharedPreferences, plus the target/current prefs.
 * Companion-level like buildTargetUrl — plain JUnit, no Robolectric (its
 * native binder does not load on arm64 Termux JVMs). Gson (an implementation
 * dependency) does the JSON work; org.json is stubbed in local unit tests.
 */
class ChatFragmentRemoteTargetsTest {
	private val desktop = ChatFragment.RemoteTarget("桌面", "192.168.1.5", 8765)
	private val nas = ChatFragment.RemoteTarget("nas", "nas.box.local", 9000)

	// ------------------------------------------------------- serialize/parse

	@Test
	fun serialize_then_parse_roundTripsEveryEntry() {
		val json = ChatFragment.serializeRemoteTargets(listOf(desktop, nas))
		assertThat(ChatFragment.parseRemoteTargets(json)).isEqualTo(listOf(desktop, nas))
	}

	@Test
	fun serialize_producesAJsonArrayOfNameHostPortObjects() {
		val json = ChatFragment.serializeRemoteTargets(listOf(desktop))
		assertThat(json).isEqualTo("""[{"name":"桌面","host":"192.168.1.5","port":8765}]""")
	}

	@Test
	fun parse_roundTripsHandWrittenJson() {
		val json = """[{"name":"桌面","host":"192.168.1.5","port":8765},
			{"name":"nas","host":"nas.box.local","port":9000}]"""
		assertThat(ChatFragment.parseRemoteTargets(json)).isEqualTo(listOf(desktop, nas))
	}

	@Test
	fun serialize_emptyList_yieldsAnEmptyArray() {
		assertThat(ChatFragment.serializeRemoteTargets(emptyList())).isEqualTo("[]")
		assertThat(ChatFragment.parseRemoteTargets("[]")).isEmpty()
	}

	// ---------------------------------------------------------- malformed in

	@Test
	fun parse_malformedJson_degradesToAnEmptyList() {
		assertThat(ChatFragment.parseRemoteTargets(null)).isEmpty()
		assertThat(ChatFragment.parseRemoteTargets("")).isEmpty()
		assertThat(ChatFragment.parseRemoteTargets("   ")).isEmpty()
		assertThat(ChatFragment.parseRemoteTargets("{")).isEmpty()
		assertThat(ChatFragment.parseRemoteTargets("null")).isEmpty()
		assertThat(ChatFragment.parseRemoteTargets("\"str\"")).isEmpty()
		assertThat(ChatFragment.parseRemoteTargets("42")).isEmpty()
	}

	@Test
	fun parse_arrayWithNonObjectElements_degradesToAnEmptyList() {
		assertThat(ChatFragment.parseRemoteTargets("""[{"name":"a","host":"h","port":1},"junk"]"""))
			.isEmpty()
	}

	@Test
	fun parse_entryWithMissingFields_yieldsDefaultsThatFailValidation() {
		val targets = ChatFragment.parseRemoteTargets("""[{"name":"x"}]""")
		assertThat(targets).isEqualTo(listOf(ChatFragment.RemoteTarget("x", "", 0)))
		assertThat(ChatFragment.validateRemoteTargets(targets)).isNotEmpty()
	}

	@Test
	fun parse_nonNumericPort_degradesToAnInvalidPort() {
		val targets = ChatFragment.parseRemoteTargets("""[{"name":"x","host":"h","port":"abc"}]""")
		assertThat(targets.first().port).isEqualTo(0)
		assertThat(ChatFragment.validateRemoteTargets(targets)).isEqualTo(ChatFragment.MSG_PORT_INVALID)
	}

	// ------------------------------------------------------------ validation

	@Test
	fun validate_acceptsAWellFormedList() {
		assertThat(ChatFragment.validateRemoteTargets(listOf(desktop, nas))).isNull()
	}

	@Test
	fun validate_blankName_rejected() {
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("  ", "h", 1))))
			.isEqualTo(ChatFragment.MSG_NAME_REQUIRED)
	}

	@Test
	fun validate_duplicateName_rejected() {
		val dup = ChatFragment.RemoteTarget("nas ", "other.host", 1234)
		assertThat(ChatFragment.validateRemoteTargets(listOf(nas, dup)))
			.isEqualTo(ChatFragment.MSG_NAME_DUPLICATE)
	}

	@Test
	fun validate_duplicateDetectionIgnoresSurroundingWhitespace() {
		val dup = ChatFragment.RemoteTarget(" 桌面", "other.host", 1234)
		assertThat(ChatFragment.validateRemoteTargets(listOf(desktop, dup)))
			.isEqualTo(ChatFragment.MSG_NAME_DUPLICATE)
	}

	@Test
	fun validate_badHosts_rejected() {
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "", 1))))
			.isEqualTo(ChatFragment.MSG_HOST_INVALID)
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "  ", 1))))
			.isEqualTo(ChatFragment.MSG_HOST_INVALID)
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "my host", 1))))
			.isEqualTo(ChatFragment.MSG_HOST_INVALID)
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "host/path", 1))))
			.isEqualTo(ChatFragment.MSG_HOST_INVALID)
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "host:8765", 1))))
			.isEqualTo(ChatFragment.MSG_HOST_INVALID)
	}

	@Test
	fun validate_badPorts_rejected() {
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "h", 0))))
			.isEqualTo(ChatFragment.MSG_PORT_INVALID)
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "h", 65536))))
			.isEqualTo(ChatFragment.MSG_PORT_INVALID)
		assertThat(ChatFragment.validateRemoteTargets(listOf(ChatFragment.RemoteTarget("a", "h", -1))))
			.isEqualTo(ChatFragment.MSG_PORT_INVALID)
	}

	@Test
	fun validate_emptyList_isValid_clearingAllRemotesIsAllowed() {
		assertThat(ChatFragment.validateRemoteTargets(emptyList())).isNull()
	}

	// ------------------------------------------------------ legacy migration

	@Test
	fun legacy_namedEntry_becomesOneArrayEntry() {
		assertThat(ChatFragment.legacyRemoteTarget("My NAS", "nas.box", 9000))
			.isEqualTo(ChatFragment.RemoteTarget("My NAS", "nas.box", 9000))
	}

	@Test
	fun legacy_blankName_fallsBackToTheHost() {
		assertThat(ChatFragment.legacyRemoteTarget("", "192.168.1.5", 8765))
			.isEqualTo(ChatFragment.RemoteTarget("192.168.1.5", "192.168.1.5", 8765))
	}

	@Test
	fun legacy_blankHost_yieldsNull_nothingToMigrate() {
		assertThat(ChatFragment.legacyRemoteTarget("My NAS", "", 8765)).isNull()
		assertThat(ChatFragment.legacyRemoteTarget("My NAS", "   ", 8765)).isNull()
	}

	// ----------------------------------------------- post-save reconciliation

	@Test
	fun reconcile_activeRemoteUntouched_needsNoChange() {
		assertThat(ChatFragment.reconcileTargetAfterSave("remote", "桌面", listOf(desktop, nas)))
			.isNull()
	}

	@Test
	fun reconcile_activeRemoteRemoved_degradesToLocal() {
		assertThat(ChatFragment.reconcileTargetAfterSave("remote", "gone", listOf(desktop, nas)))
			.isEqualTo(ChatFragment.TARGET_LOCAL)
	}

	@Test
	fun reconcile_localTarget_neverChanges() {
		assertThat(ChatFragment.reconcileTargetAfterSave("local", "gone", listOf(desktop))).isNull()
		assertThat(ChatFragment.reconcileTargetAfterSave("local", "gone", emptyList())).isNull()
	}

	@Test
	fun reconcile_emptyList_degradesToLocal() {
		assertThat(ChatFragment.reconcileTargetAfterSave("remote", "桌面", emptyList()))
			.isEqualTo(ChatFragment.TARGET_LOCAL)
	}
}
