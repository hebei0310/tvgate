package updater

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/qist/tvgate/config"
	"github.com/qist/tvgate/utils/upgrade"
)

var (
	statusMutex sync.RWMutex
	statusMap   = map[string]string{"state": "idle", "message": ""}
)

// --- 升级状态管理 ---
func SetStatus(state, message string) {
	statusMutex.Lock()
	defer statusMutex.Unlock()
	statusMap["state"] = state
	statusMap["message"] = message
}

func GetStatus() map[string]string {
	statusMutex.RLock()
	defer statusMutex.RUnlock()
	cpy := make(map[string]string)
	for k, v := range statusMap {
		cpy[k] = v
	}
	return cpy
}

// --- Release 信息 ---
type Release struct {
	TagName string `json:"tag_name"`
}

func buildURL(base, target string) string {
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	if len(target) > 0 && target[0] == '/' {
		target = target[1:]
	}
	return base + "/" + target
}

func FetchGithubReleases(cfg config.GithubConfig) ([]Release, error) {
	var urls []string
	apiPath := "https://api.github.com/repos/qist/tvgate/releases"

	if cfg.Enabled {
		if cfg.URL != "" {
			urls = append(urls, buildURL(cfg.URL, apiPath))
		}
		for _, b := range cfg.BackupURLs {
			if b != "" {
				urls = append(urls, buildURL(b, apiPath))
			}
		}
	}

	// 官方 URL 兜底
	urls = append(urls, apiPath)

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: timeout}

	var lastErr error
	for _, url := range urls {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "TVGate-Updater")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("请求返回错误状态码 %d", resp.StatusCode)
			continue
		}

		var releases []Release
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			lastErr = err
			continue
		}

		return releases, nil
	}

	return nil, lastErr
}

// --- 系统架构 ---
type ArchInfo struct {
	GOOS        string
	GOARCH      string
	GOARM       string
	PackageArch string
}

func GetArchInfo() (*ArchInfo, error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	var arch ArchInfo

	switch fmt.Sprintf("%s-%s", goos, goarch) {
	case "linux-amd64":
		arch = ArchInfo{"linux", "amd64", "", "amd64"}
	case "linux-arm64":
		arch = ArchInfo{"linux", "arm64", "", "arm64"}
	case "linux-arm":
		goarm := os.Getenv("GOARM")
		if goarm == "" {
			goarm = "7"
		}
		arch = ArchInfo{"linux", "arm", goarm, "armv" + goarm}
	case "linux-386":
		arch = ArchInfo{"linux", "386", "", "386"}
	case "windows-amd64":
		arch = ArchInfo{"windows", "amd64", "", "amd64"}
	case "windows-386":
		arch = ArchInfo{"windows", "386", "", "386"}
	case "darwin-amd64":
		arch = ArchInfo{"darwin", "amd64", "", "amd64"}
	case "darwin-arm64":
		arch = ArchInfo{"darwin", "arm64", "", "arm64"}
	default:
		return nil, fmt.Errorf("unsupported OS/ARCH: %s-%s", goos, goarch)
	}
	return &arch, nil
}

func getDownloadURLs(cfg config.GithubConfig, version, zipFileName string) []string {
	urls := []string{}
	origURL := fmt.Sprintf("https://github.com/qist/tvgate/releases/download/%s/%s", version, zipFileName)

	if cfg.Enabled {
		if cfg.URL != "" {
			urls = append(urls, buildURL(cfg.URL, origURL))
		}
		for _, b := range cfg.BackupURLs {
			if b != "" {
				urls = append(urls, buildURL(b, origURL))
			}
		}
	}
	urls = append(urls, origURL)
	return urls
}

// --------------------
// 下载 + 解压 + 平滑升级
// --------------------
func UpdateFromGithub(cfg config.GithubConfig, version string) error {
	SetStatus("starting", "开始升级流程")

	arch, err := GetArchInfo()
	if err != nil {
		SetStatus("error", fmt.Sprintf("获取系统架构信息失败: %v", err))
		return err
	}

	zipFileName := fmt.Sprintf("TVGate-%s-%s.zip", arch.GOOS, arch.PackageArch)
	urls := getDownloadURLs(cfg, version, zipFileName)
	tmpFile := filepath.Join(os.TempDir(), zipFileName)

	SetStatus("downloading", "开始下载")
	success := false
	var lastErr error
	for _, u := range urls {
		if err := downloadFile(u, tmpFile); err != nil {
			lastErr = err
			continue
		}
		success = true
		break
	}
	if !success {
		SetStatus("error", fmt.Sprintf("所有下载源失败: %v", lastErr))
		return lastErr
	}

	// 备份当前程序
	SetStatus("backing_up", "备份当前程序")
	execPath, err := os.Executable()
	if err != nil {
		SetStatus("error", fmt.Sprintf("获取可执行文件路径失败: %v", err))
		return err
	}
	
	backupDir := filepath.Join(filepath.Dir(execPath), "tvgate-backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		SetStatus("error", fmt.Sprintf("创建备份目录失败: %v", err))
		return err
	}

	backupName := filepath.Base(execPath)
	backupPath := filepath.Join(backupDir, backupName)

	// 将当前可执行文件移动到备份目录
	// 在Windows系统上，正在运行的文件无法被删除或覆盖，所以必须先移动
	if err := os.Rename(execPath, backupPath); err != nil {
		SetStatus("error", fmt.Sprintf("备份失败: %v", err))
		return err
	}
	
	// 设置备份文件权限
	_ = os.Chmod(backupPath, 0755)

	// 解压新版本
	SetStatus("unzipping", "解压新版本")
	tmpDestDir := filepath.Join(filepath.Dir(execPath), ".tmp_upgrade")
	_ = os.RemoveAll(tmpDestDir)
	_ = os.MkdirAll(tmpDestDir, 0755)

	if err := unzip(tmpFile, tmpDestDir); err != nil {
		SetStatus("error", fmt.Sprintf("解压失败: %v", err))
		return err
	}

	// 新可执行文件路径
	newapp := fmt.Sprintf("TVGate-%s-%s", arch.GOOS, arch.PackageArch)
	if runtime.GOOS == "windows" {
		newapp += ".exe"
	}
	newExecPath := filepath.Join(tmpDestDir, filepath.Base(newapp))
	if runtime.GOOS != "windows" {
		_ = os.Chmod(newExecPath, 0755)
	}

	// 使用 tableflip 升级进程
	SetStatus("restarting", "重启新版本")
	SetStatus("success", "升级成功，正在重启")
	upgrade.UpgradeProcess(newExecPath, *config.ConfigFilePath, tmpDestDir)

	return nil
}

// --------------------
// 工具函数
// --------------------
func downloadFile(url, dst string) error {
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		path := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(path, f.Mode())
			continue
		}
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, _ = io.Copy(out, rc)
		rc.Close()
		out.Close()
	}
	return nil
}
