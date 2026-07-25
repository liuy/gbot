import org.gradle.api.tasks.testing.Test

plugins {
	id("com.android.application")
	id("org.jetbrains.kotlin.android")
	id("jacoco")
}

android {
	namespace = "com.gbot.android"
	compileSdk = 34

	defaultConfig {
		applicationId = "com.gbot.android.remote"
		minSdk = 30
		targetSdk = 34
		versionCode = 1
		versionName = "1.0.0"
	}

	buildTypes {
		release {
			isMinifyEnabled = true
			proguardFiles(
				getDefaultProguardFile("proguard-android-optimize.txt"),
				"proguard-rules.pro"
			)
		}
	}

	compileOptions {
		sourceCompatibility = JavaVersion.VERSION_17
		targetCompatibility = JavaVersion.VERSION_17
	}

	kotlinOptions {
		jvmTarget = "17"
	}

	buildFeatures {
		viewBinding = true
	}

	testOptions {
		unitTests {
			isIncludeAndroidResources = true
		}
	}
}

// Jacoco offline instrumentation.
// Robolectric loads app classes through AndroidSandboxClassLoader, which bypasses the
// Jacoco runtime agent's class-file transformer, so app code never gets probed. We
// inject probes into the compiled .class files at build time; the loaded classes then
// write execution data through the agent at JVM exit.
val jacocoVersion = "0.8.8"
// tmp/kotlin-classes/debug is what plain JVM unit tests (non-Robolectric) load app
// classes from. Robolectric tests instead resolve app classes from the AGP-bundled
// runtime_app_classes_jar (see below), so BOTH must hold probed bytecode. The old
// approach mutated the test task's classpath in doFirst to prepend a side dir, but that
// is racy: AGP finalizes the classpath early and the loose dir vs. bundled-jar ordering
// that Robolectric's sandbox sees is not guaranteed, so coverage was non-deterministic.
val originalClassesDir = layout.buildDirectory.dir("tmp/kotlin-classes/debug")
// Un-instrumented copy of the originals. Jacoco's InstrumentTask cannot read from and
// write to the same directory (it truncates files mid-stream), and the report must run
// against un-instrumented classes (probed bytecode inflates instruction/line counts).
// So: snapshot originals here, report reads here, and instrumentation goes
// staging -> tmp/kotlin-classes/debug.
val originalClassesStagingDir = layout.buildDirectory.dir("tmp/jacoco-original-classes/debug")
val execFile = layout.buildDirectory.file("jacoco/testDebugUnitTest.exec")

// Same excludes the report uses, so generated/R/binding classes are not instrumented.
val coverageFileFilter = listOf(
	"**/R.class",
	"**/R$*.class",
	"**/BuildConfig.*",
	"**/Manifest*.*",
	"**/*Test.*",
	"androidx/**/*",
	"**/*\$ViewBinder*",
	"**/databinding/*",
	"**/*Binding.*"
)

dependencies {
	implementation("androidx.core:core-ktx:1.12.0")
	implementation("androidx.appcompat:appcompat:1.6.1")
	implementation("androidx.fragment:fragment-ktx:1.6.2")
	implementation("com.google.android.material:material:1.11.0")
	implementation("androidx.constraintlayout:constraintlayout:2.1.4")
	implementation("androidx.preference:preference-ktx:1.2.1")
	implementation("org.java-websocket:Java-WebSocket:1.5.6")
	implementation("com.google.code.gson:gson:2.10.1")
	implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.7.3")

	testImplementation("junit:junit:4.13.2")
	testImplementation("com.google.truth:truth:1.4.2")
	testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.7.3")
	testImplementation("org.robolectric:robolectric:4.11.1")
	testImplementation("androidx.test:core:1.5.0")
	testImplementation("androidx.test.ext:junit:1.1.5")

	jacocoAnt("org.jacoco:org.jacoco.ant:$jacocoVersion")
}

// Runs Jacoco's offline "instrument" ant task on the compiled main classes. The
// instrumented output is written back into tmp/kotlin-classes/debug, and the AGP-bundled
// runtime app-class jar is repackaged from those probed classes. Robolectric's sandbox
// resolves app classes from that runtime jar (not the loose classes dir), so both must
// hold probed bytecode for coverage to be recorded. Instrumentation reads from the
// staging copy (InstrumentTask truncates files when src==dst), and staging also feeds the
// report, which must run against un-instrumented classes. The compile app-class jar is
// left untouched: it feeds test COMPILATION and probed bytecode (with jacoco RT refs)
// breaks the Kotlin compiler.
val runtimeAppClassesJar = layout.buildDirectory.file("intermediates/runtime_app_classes_jar/debug/classes.jar")

tasks.register("jacocoOfflineInstrument") {
	val dst = originalClassesDir
	val staging = originalClassesStagingDir
	val runtimeJar = runtimeAppClassesJar
	val filter = coverageFileFilter
	val antCfg = configurations.named("jacocoAnt")
	dependsOn("compileDebugKotlin")
	dependsOn("bundleDebugClassesToRuntimeJar")
	inputs.dir(dst)
	inputs.files(antCfg)
	outputs.dir(staging)
	// We overwrite the consumed dir + runtime jar in place, which Gradle cannot track as
	// an output. On a re-run where compile/bundle are up-to-date the inputs would look
	// unchanged and Gradle would skip re-instrumenting, leaving a stale mix. Always
	// re-instrument so the consumed artifacts and staging copy stay coherent.
	outputs.upToDateWhen { false }
	doLast {
		val dstDir = dst.get().asFile
		val stagingDir = staging.get().asFile
		val classpath = antCfg.get().asPath
		// Snapshot the un-instrumented originals first; report + instrumentation read these.
		stagingDir.deleteRecursively()
		dstDir.copyRecursively(stagingDir, overwrite = true)
		// Instrument staging -> the consumed dir. Wipe dst first so stale classes from a
		// prior build don't leak through, then rewrite it with probed classes.
		dstDir.deleteRecursively()
		dstDir.mkdirs()
		ant.withGroovyBuilder {
			"taskdef"(
				"name" to "jacoco-instrument",
				"classname" to "org.jacoco.ant.InstrumentTask",
				"classpath" to classpath
			)
			"jacoco-instrument"("destdir" to dstDir.absolutePath) {
				"fileset"("dir" to stagingDir.absolutePath) {
					for (entry in filter) {
						"exclude"("name" to entry)
					}
				}
			}
		}
		// Repackage the AGP runtime app-class jar with the now-probed classes, so
		// Robolectric's sandbox classloader (which reads this jar) also sees probes. The jar
		// also holds R/databinding classes the loose dir lacks, so update it in place
		// (replace matching entries) rather than rebuilding from the loose dir only.
		val jarPath = runtimeJar.get().asFile
		ant.withGroovyBuilder {
			"jar"("destfile" to jarPath.absolutePath, "update" to "true") {
				"fileset"("dir" to dstDir.absolutePath)
			}
		}
	}
}

// Wire offline instrumentation into the AGP test task. jacocoOfflineInstrument has
// already put probed bytecode into both the loose classes dir and the runtime app-class
// jar (what plain JVM tests and Robolectric load respectively), so no classpath reorder
// is needed: every code path that can load app classes sees probes. The jacoco plugin's
// JacocoTaskExtension attaches the runtime agent that the offline probes talk to and
// writes execution data to a fixed exec file. We must NOT add a second -javaagent: a
// duplicate premain crashes the JVM with a duplicate java.lang.$JaCoCo class definition.
gradle.projectsEvaluated {
	tasks.named<Test>("testDebugUnitTest") {
		dependsOn("jacocoOfflineInstrument")
		val execPathProvider = execFile
		// The plugin's JacocoTaskExtension attaches Gradle's bundled jacoco agent, which
		// provides the RT runtime the offline probes call into. Route execution data to
		// our fixed exec file.
		extensions.configure<JacocoTaskExtension>("jacoco") {
			isEnabled = true
			destinationFile = execPathProvider.get().asFile
		}
	}
}

tasks.register<JacocoReport>("jacocoTestReport") {
	dependsOn("testDebugUnitTest")
	reports {
		xml.required.set(true)
		html.required.set(true)
	}
	// Report against the un-instrumented staging copy. tmp/kotlin-classes/debug now
	// holds probed bytecode (instrumented in place), so reading it here would make
	// Jacoco count inserted probe fields/instructions and distort the numbers.
	val debugTree = fileTree(originalClassesStagingDir) {
		exclude(coverageFileFilter)
	}
	classDirectories.setFrom(debugTree)
	sourceDirectories.setFrom(files("${project.projectDir}/src/main/kotlin"))
	executionData.setFrom(files(execFile))
}
