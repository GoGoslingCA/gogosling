package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/secret"
)

// openSecrets 按数据目录记录的后端打开凭据存储（secret.OpenWith）；GOGOSLING_SECRET_BACKEND
// 给出 keyring / file 时显式切换（并更新记录），其他非空值返回 CodeInvalid。
// 记录的后端不可用时返回 CodeUnavailable，消息提示如何显式切换，不静默改用另一个后端。
func openSecrets(ctx context.Context, dataDir string, getenv func(string) string, log *slog.Logger) (secret.Store, error) {
	backend := strings.ToLower(strings.TrimSpace(getenv(config.EnvSecretBackend)))
	switch backend {
	case "", secret.BackendKeyring, secret.BackendFile:
	default:
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "环境变量 " + config.EnvSecretBackend + " 只能是 keyring 或 file",
			Details: map[string]any{"env": config.EnvSecretBackend, "value": backend},
		}
	}
	s, err := secret.OpenWith(ctx, dataDir, log, secret.Options{Backend: backend})
	if err != nil {
		return nil, mapSecretErr(err, "")
	}
	return s, nil
}

// secretAdapter 把叶子包 secret 的哨兵错误映射为 model.Code，供 provider（SecretStore）、
// gateway（SecretReader）、channel（Secrets）与 MCP 拨号器（EnvResolver）使用。
// 这是全仓唯一的一份映射（channel 等领域包只处理映射后的 model.Code）。
//
// 映射：ErrNotFound → not_found；ErrInvalidName / ErrTooLarge → invalid；
// ErrBackendUnavailable → unavailable（附显式切换后端的提示）；
// ErrCorrupt / ErrKeyMissing 及其他后端故障 → unavailable；ctx 取消 / 超时 → cancelled / timeout。
// 错误消息只含凭据名，永远不含凭据值。
type secretAdapter struct {
	s secret.Store
}

// secretAdapter 自身也满足 secret.Store：错误带 model.Code，同时仍可用 errors.Is 匹配原哨兵错误。
var _ secret.Store = secretAdapter{}

// Get 读取凭据。
func (a secretAdapter) Get(ctx context.Context, name string) (string, error) {
	v, err := a.s.Get(ctx, name)
	if err != nil {
		return "", mapSecretErr(err, name)
	}
	return v, nil
}

// Set 写入凭据。
func (a secretAdapter) Set(ctx context.Context, name, value string) error {
	return mapSecretErr(a.s.Set(ctx, name, value), name)
}

// Delete 删除凭据；不存在视为成功。
func (a secretAdapter) Delete(ctx context.Context, name string) error {
	err := a.s.Delete(ctx, name)
	if errors.Is(err, secret.ErrNotFound) {
		return nil
	}
	return mapSecretErr(err, name)
}

// Has 报告凭据是否存在。
func (a secretAdapter) Has(ctx context.Context, name string) (bool, error) {
	ok, err := a.s.Has(ctx, name)
	if err != nil {
		return false, mapSecretErr(err, name)
	}
	return ok, nil
}

// Resolve 满足 mcp.EnvResolver：把 "secret:<ref>" 中的 ref 解析为凭据值。
func (a secretAdapter) Resolve(ctx context.Context, ref string) (string, error) {
	return a.Get(ctx, ref)
}

// mapSecretErr 把 secret 包的错误映射为带 model.Code 的错误；err 为 nil 时返回 nil。
func mapSecretErr(err error, name string) error {
	if err == nil {
		return nil
	}
	details := map[string]any{}
	if name != "" {
		details["secret"] = name
	}
	var me *model.Error
	switch {
	case errors.As(err, &me):
		return err
	case errors.Is(err, context.Canceled):
		return &model.Error{Code: model.CodeCancelled, Message: "凭据操作被取消", Details: details, Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &model.Error{Code: model.CodeTimeout, Message: "凭据操作超时", Details: details, Err: err}
	case errors.Is(err, secret.ErrNotFound):
		return &model.Error{Code: model.CodeNotFound, Message: "凭据不存在", Details: details, Err: err}
	case errors.Is(err, secret.ErrInvalidName):
		return &model.Error{Code: model.CodeInvalid, Message: "凭据名称不合法", Details: details, Err: err}
	case errors.Is(err, secret.ErrTooLarge):
		return &model.Error{Code: model.CodeInvalid, Message: "凭据内容过大", Details: details, Err: err}
	case errors.Is(err, secret.ErrBackendUnavailable):
		details["env"] = config.EnvSecretBackend
		return &model.Error{
			Code: model.CodeUnavailable,
			Message: "数据目录记录的凭据后端当前不可用；为避免已存的凭据“消失”，不会自动改用其他后端。" +
				"请在可以访问该后端的环境（例如桌面会话）中启动，或设置 " + config.EnvSecretBackend + "=file 显式改用加密文件",
			Details: details,
			Err:     err,
		}
	case errors.Is(err, secret.ErrCorrupt), errors.Is(err, secret.ErrKeyMissing):
		return &model.Error{Code: model.CodeUnavailable, Message: "凭据存储已损坏或缺少密钥", Details: details, Err: err}
	default:
		return &model.Error{Code: model.CodeUnavailable, Message: "凭据存储不可用", Details: details, Err: err}
	}
}
