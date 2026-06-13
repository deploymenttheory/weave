// Port of tart's VNC/FullFledgedVNC.swift: Virtualization.Framework's
// private VNC server (_VZVNCServer). Swift's Dynamic package becomes plain
// ObjC runtime sends.
//go:build darwin

package vnc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/passphrase"
	weavevm "github.com/deploymenttheory/weave/internal/vm"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	dispatch "github.com/deploymenttheory/go-bindings-macosplatform/internal/objc"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
)

// FullFledgedVNC ports tart's FullFledgedVNC class.
type FullFledgedVNC struct {
	Password string
	vnc      objc.ID
}

var _ VNC = (*FullFledgedVNC)(nil)

// NewFullFledgedVNC ports FullFledgedVNC.init(virtualMachine:). An empty
// password means a generated 4-word passphrase, as before; a non-empty one
// (lume's --vnc-password) is used verbatim, which the setup automation needs
// to dial back in with a known credential.
func NewFullFledgedVNC(vm *weavevm.VM, password string) *FullFledgedVNC {
	if password == "" {
		password = strings.Join(passphrase.GeneratePassphrase(4), "-")
	}

	var vnc objc.ID
	dispatch.RunOnMainThread(func() {
		securityConfiguration := objc.ID(objc.GetClass("_VZVNCAuthenticationSecurityConfiguration")).
			Send(objc.RegisterName("alloc")).
			Send(objc.RegisterName("initWithPassword:"), pureobjc.NSString(password))

		globalQueue := dispatchGetGlobalQueue()
		vnc = objc.ID(objc.GetClass("_VZVNCServer")).
			Send(objc.RegisterName("alloc")).
			Send(objc.RegisterName("initWithPort:queue:securityConfiguration:"),
				uint16(0), globalQueue, securityConfiguration)
		vnc.Send(objc.RegisterName("setVirtualMachine:"), vm.VirtualMachine.Ptr())
		vnc.Send(objc.RegisterName("start"))
	})

	return &FullFledgedVNC{Password: password, vnc: vnc}
}

func (v *FullFledgedVNC) WaitForURL(ctx context.Context, netBridged bool) (*foundation.NSURL, error) {
	for {
		// Port is 0 shortly after start(), but will be initialized later.
		var port uint16
		dispatch.RunOnMainThread(func() {
			port = objc.Send[uint16](v.vnc, objc.RegisterName("port"))
		})
		if port != 0 {
			return foundation.NSURLURLWithString(objcutil.NSStr(
				fmt.Sprintf("vnc://:%s@127.0.0.1:%d", v.Password, port))), nil
		}

		// Wait 50 ms.
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (v *FullFledgedVNC) Stop() error {
	dispatch.RunOnMainThread(func() {
		v.vnc.Send(objc.RegisterName("stop"))
	})
	return nil
}

// dispatchGetGlobalQueue returns dispatch_get_global_queue(0, 0), resolved
// from libdispatch at runtime via purego.
var dispatchGetGlobalQueue = sync.OnceValue(func() objc.ID {
	var getGlobalQueue func(identifier int64, flags uint64) uintptr
	symbol, err := purego.Dlsym(purego.RTLD_DEFAULT, "dispatch_get_global_queue")
	if err != nil || symbol == 0 {
		return 0
	}
	purego.RegisterFunc(&getGlobalQueue, symbol)
	return objc.ID(getGlobalQueue(0, 0))
})
