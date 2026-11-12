// 把 nacos-sdk-go/v2 的 config client 适配成 Kratos v3 的 config.Source / config.Watcher。
package nacos

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-kratos/kratos/v3/config"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

type Option func(*options)

type options struct {
	group  string
	dataID string
}

// WithGroup 设置 nacos group。
func WithGroup(group string) Option {
	return func(o *options) { o.group = group }
}

// WithDataID 设置 nacos dataId。
func WithDataID(dataID string) Option {
	return func(o *options) { o.dataID = dataID }
}

// Source 把 nacos 一条配置暴露给 Kratos config。
type Source struct {
	opts   options
	client config_client.IConfigClient

	mu      sync.Mutex
	content string
}

// NewSource 构造 Source。
func NewSource(client config_client.IConfigClient, opts ...Option) config.Source {
	_opts := options{}
	for _, o := range opts {
		o(&_opts)
	}
	return &Source{opts: _opts, client: client}
}

// Load 从 nacos 拉一次配置。
func (s *Source) Load() ([]*config.KeyValue, error) {
	content, err := s.client.GetConfig(vo.ConfigParam{
		DataId: s.opts.dataID,
		Group:  s.opts.group,
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.content = content
	s.mu.Unlock()
	return []*config.KeyValue{{
		Key:    s.opts.dataID,
		Value:  []byte(content),
		Format: strings.TrimPrefix(filepath.Ext(s.opts.dataID), "."),
	}}, nil
}

// Watch 注册 nacos 监听，回调通过 channel 通知 Kratos 重 Load。
func (s *Source) Watch() (config.Watcher, error) {
	w := &watcher{
		dataID:    s.opts.dataID,
		group:     s.opts.group,
		client:    s.client,
		contentCh: make(chan string, 1),
	}
	err := s.client.ListenConfig(vo.ConfigParam{
		DataId: s.opts.dataID,
		Group:  s.opts.group,
		OnChange: func(_, group, dataId, data string) {
			w.publish(data)
		},
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// watcher 把 nacos OnChange 转成 Kratos config.Watcher。
type watcher struct {
	dataID    string
	group     string
	client    config_client.IConfigClient
	contentCh chan string
}

// publish 把新配置塞进 channel，不阻塞。缓冲区满则丢掉旧值（最新值优先）。
func (w *watcher) publish(data string) {
	for {
		select {
		case w.contentCh <- data:
			return
		default:
			select {
			case <-w.contentCh:
			default:
			}
		}
	}
}

// Next 阻塞直到 nacos 推变更。
func (w *watcher) Next() ([]*config.KeyValue, error) {
	data := <-w.contentCh
	return []*config.KeyValue{{
		Key:    w.dataID,
		Value:  []byte(data),
		Format: strings.TrimPrefix(filepath.Ext(w.dataID), "."),
	}}, nil
}

// Stop 取消 nacos 监听。
func (w *watcher) Stop() error {
	return w.client.CancelListenConfig(vo.ConfigParam{
		DataId: w.dataID,
		Group:  w.group,
	})
}
