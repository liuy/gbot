//go:build android

package main

/*
#cgo LDFLAGS: -llog
#include <jni.h>

static const char* jstring_to_cstr(JNIEnv* env, jstring s) {
    if (s == NULL) return NULL;
    return (*env)->GetStringUTFChars(env, s, NULL);
}
static void release_jstring(JNIEnv* env, jstring s, const char* cstr) {
    if (s != NULL && cstr != NULL) (*env)->ReleaseStringUTFChars(env, s, cstr);
}
*/
import "C"

import (
	"log/slog"
	"sync"
)

var dataPathOnce sync.Once

//export Java_com_wails_app_WailsBridge_nativeSetDataPath
func Java_com_wails_app_WailsBridge_nativeSetDataPath(env *C.JNIEnv, obj C.jobject, jpath C.jstring) {
	cstr := C.jstring_to_cstr(env, jpath)
	defer C.release_jstring(env, jpath, cstr)
	if cstr == nil {
		return
	}
	path := C.GoString(cstr)
	dataPathOnce.Do(func() {
		applyAndroidEnv(path)
		slog.Info("android: data path set", "HOME", path)
	})
}
