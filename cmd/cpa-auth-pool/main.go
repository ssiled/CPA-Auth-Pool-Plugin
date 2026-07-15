package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { uint8_t* data; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

typedef struct { uint32_t abi_version; } cliproxy_host_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"unsafe"

	"cpa-auth-pool/internal/plugin"
)

var app = plugin.NewApp()

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) C.int {
	if api == nil {
		return 1
	}
	api.abi_version = C.uint32_t(plugin.ABIVersion)
	api.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	api.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	api.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, data *C.uint8_t, length C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	request := C.GoBytes(unsafe.Pointer(data), C.int(length))
	raw, err := app.HandleMethod(C.GoString(method), request)
	if err != nil {
		raw = plugin.ErrorEnvelope("plugin_error", err.Error(), 500)
	}
	ptr := C.malloc(C.size_t(len(raw)))
	if ptr == nil && len(raw) > 0 {
		return 1
	}
	if len(raw) > 0 {
		copy(unsafe.Slice((*byte)(ptr), len(raw)), raw)
	}
	response.data = (*C.uint8_t)(ptr)
	response.len = C.size_t(len(raw))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) { C.free(ptr) }

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { app.Shutdown() }

func main() {}
