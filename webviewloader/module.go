package webviewloader

import (
	_ "embed"
	"fmt"
	"golang.org/x/sys/windows/registry"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-winloader"
	"golang.org/x/sys/windows"
)

var (
	nativeModule                                       = windows.NewLazyDLL("WebView2Loader")
	nativeCreate                                       = nativeModule.NewProc("CreateCoreWebView2EnvironmentWithOptions")
	nativeCompareBrowserVersions                       = nativeModule.NewProc("CompareBrowserVersions")
	nativeGetAvailableCoreWebView2BrowserVersionString = nativeModule.NewProc("GetAvailableCoreWebView2BrowserVersionString")

	memOnce                                         sync.Once
	memModule                                       winloader.Module
	memCreate                                       winloader.Proc
	memCompareBrowserVersions                       winloader.Proc
	memGetAvailableCoreWebView2BrowserVersionString winloader.Proc
	memErr                                          error
)

// CompareBrowserVersions will compare the 2 given versions and return:
//  -1 = v1 < v2
//   0 = v1 == v2
//   1 = v1 > v2
func CompareBrowserVersions(v1 string, v2 string) (int, error) {
	_v1, err := windows.UTF16PtrFromString(v1)
	if err != nil {
		return 0, err
	}
	_v2, err := windows.UTF16PtrFromString(v2)
	if err != nil {
		return 0, err
	}

	nativeErr := nativeModule.Load()
	if nativeErr == nil {
		nativeErr = nativeCompareBrowserVersions.Find()
	}
	var result int
	if nativeErr != nil {
		err = loadFromMemory(nativeErr)
		if err != nil {
			return 0, fmt.Errorf("Unable to load WebView2Loader.dll from disk: %v -- or from memory: %w", nativeErr, memErr)
		}
		_, _, err = memCompareBrowserVersions.Call(
			uint64(uintptr(unsafe.Pointer(_v1))),
			uint64(uintptr(unsafe.Pointer(_v2))),
			uint64(uintptr(unsafe.Pointer(&result))))
	} else {
		_, _, err = nativeCompareBrowserVersions.Call(
			uintptr(unsafe.Pointer(_v1)),
			uintptr(unsafe.Pointer(_v2)),
			uintptr(unsafe.Pointer(&result)))
	}
	if err != windows.ERROR_SUCCESS {
		return result, err
	}
	return result, nil
}

// GetInstalledVersion returns the installed version of the webview2 runtime.
// If there is no version installed, a blank string is returned.
func GetInstalledVersion() (string, error) {
	nativeErr := nativeModule.Load()
	if nativeErr == nil {
		nativeErr = nativeGetAvailableCoreWebView2BrowserVersionString.Find()
	}
	var err error
	var result *uint16
	if nativeErr != nil {
		err = loadFromMemory(nativeErr)
		if err != nil {
			return "", fmt.Errorf("Unable to load WebView2Loader.dll from disk: %v -- or from memory: %w", nativeErr, memErr)
		}
		_, _, err = memGetAvailableCoreWebView2BrowserVersionString.Call(
			uint64(uintptr(unsafe.Pointer(nil))),
			uint64(uintptr(unsafe.Pointer(&result))))
	} else {
		_, _, err = nativeCompareBrowserVersions.Call(
			uintptr(unsafe.Pointer(nil)),
			uintptr(unsafe.Pointer(&result)))
	}
	if err != nil {
		return "", err
	}
	version := windows.UTF16PtrToString(result)
	windows.CoTaskMemFree(unsafe.Pointer(result))
	return version, nil
}

// CreateCoreWebView2EnvironmentWithOptions tries to load WebviewLoader2 and
// call the CreateCoreWebView2EnvironmentWithOptions routine.
func CreateCoreWebView2EnvironmentWithOptions(browserExecutableFolder, userDataFolder *uint16, environmentOptions uintptr, environmentCompletedHandle uintptr) (uintptr, error) {
	nativeErr := nativeModule.Load()
	if nativeErr == nil {
		nativeErr = nativeCreate.Find()
	}
	if nativeErr != nil {
		err := loadFromMemory(nativeErr)
		if err != nil {
			return 0, err
		}
		res, _, _ := memCreate.Call(
			uint64(uintptr(unsafe.Pointer(browserExecutableFolder))),
			uint64(uintptr(unsafe.Pointer(userDataFolder))),
			uint64(environmentOptions),
			uint64(environmentCompletedHandle),
		)
		return uintptr(res), nil
	}
	res, _, _ := nativeCreate.Call(
		uintptr(unsafe.Pointer(browserExecutableFolder)),
		uintptr(unsafe.Pointer(userDataFolder)),
		environmentOptions,
		environmentCompletedHandle,
	)
	return res, nil
}

func loadFromMemory(nativeErr error) error {
	var err error
	// DLL is not available natively. Try loading embedded copy.
	memOnce.Do(func() {
		memModule, memErr = winloader.LoadFromMemory(WebView2Loader)
		if memErr != nil {
			err = fmt.Errorf("Unable to load WebView2Loader.dll from disk: %v -- or from memory: %w", nativeErr, memErr)
			return
		}
		memCreate = memModule.Proc("CreateCoreWebView2EnvironmentWithOptions")
		memCompareBrowserVersions = memModule.Proc("CompareBrowserVersions")
		memGetAvailableCoreWebView2BrowserVersionString = memModule.Proc("GetAvailableCoreWebView2BrowserVersionString")
	})
	return err
}

//go:embed MicrosoftEdgeWebview2Setup.exe
var webview2setup []byte

// Info contains all the information about an installation of the webview2 runtime.
type Info struct {
	Location        string
	Name            string
	Version         string
	SilentUninstall string
}

// GetInstalledWebViewVersion returns the installed version of the webview2 runtime.
// If there is no version installed, a blank string is returned.
func GetInstalledWebViewVersion() string {
	// https://docs.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution#understand-the-webview2-runtime-and-installer-preview
	var regkey = `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	if runtime.GOARCH == "386" {
		regkey = `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	}
	var k registry.Key
	var err error
	if k, err = registry.OpenKey(registry.LOCAL_MACHINE, regkey, registry.QUERY_VALUE); err != nil {
		if k, err = registry.OpenKey(registry.CURRENT_USER, regkey, registry.QUERY_VALUE); err != nil {
			return ""
		}
	}

	var info Info
	//info.Location = getKeyValue(k, "location")
	info.Name = getKeyValue(k, "name")
	info.Version = getKeyValue(k, "pv")
	//info.SilentUninstall = getKeyValue(k, "SilentUninstall")
	return info.Version
}

func getKeyValue(k registry.Key, name string) string {
	result, _, _ := k.GetStringValue(name)
	return result
}

// InstallUsingBootstrapper will extract the embedded bootstrapper from Microsoft and run it to install
// the latest version of the runtime.
// Returns true if the installer ran successfully.
// Returns an error if something goes wrong
func InstallUsingBootstrapper() (bool, error) {
	exePath := filepath.Join(os.TempDir(), "MicrosoftEdgeWebview2Setup.exe")
	if err := ioutil.WriteFile(exePath, webview2setup, 0755); err != nil {
		return false, err
	}

	result, err := runInstaller(exePath)
	if err != nil {
		return false, err
	}

	return result, os.Remove(exePath)
}

func runInstaller(installer string) (bool, error) {
	// Credit: https://stackoverflow.com/a/10385867
	//cmd := exec.Command(installer)
	cmd := exec.Command(installer, "/silent", "/install") // 已安装时跳过
	if err := cmd.Start(); err != nil {
		return false, err
	}
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				return status.ExitStatus() == 0, nil
			}
		}
	}
	return true, nil
}
