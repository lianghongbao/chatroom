package oss

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/google/uuid"
)

// V4 PostObject 直传：policy 里带 content-length-range，OSS 服务端强制文件大小上限。
//
// 为什么用 PostObject 而不是预签名 PUT：
// 预签名 PUT 的签名只覆盖 canonical request，**没法表达文件长度上限** ——
// 服务端收到多大就收多大，只能事后靠 HeadObject 校验（已经落盘了，白费存储和流量）。
// PostObject 的 policy 有 content-length-range 条件，OSS 在收数据前就拒掉超限请求。
//
// 官方文档：
//   - PostObject：https://www.alibabacloud.com/help/zh/oss/developer-reference/postobject
//   - V4 签名：https://www.alibabacloud.com/help/zh/oss/developer-reference/signature-version-4-recommend
//
// V4 policy 的 conditions 里 content-length-range 的写法是**扁平三元组**：
//
//	["content-length-range", min, max]
//
// 不是 V1 那种 {"content-length-range": [min, max]} 的对象形式。
// 这是 V4 policy 唯一特殊的匹配语义 —— 它不带 "$" 前缀，不是针对某个表单域的，
// 而是针对「文件长度」这个全局量。其余条件都是针对表单域的。
//
// V4 支持的完整匹配语义（全部来自官方文档）：
//
//	content-length-range          限定文件长度区间，两端都是闭区间
//	eq / eq-ci                   表单域精确匹配（-ci 忽略大小写）
//	starts-with / starts-with-ci 表单域前缀匹配
//	in / in-ci                   表单域在字符串列表内
//	not-in / not-in-ci           表单域不在字符串列表内
//
// 同一个表单域配了多个条件时，以**最后出现**的为准。

// postProduct V4 签名里的 service 标识。
const postProduct = "oss"

// PostUploadForm 客户端拼 PostObject 表单需要的全部字段。
type PostUploadForm struct {
	// Host 上传地址，例 "https://my-bucket.oss-cn-hangzhou.aliyuncs.com"。
	Host string
	// Policy Base64 编码后的 policy 字符串。
	Policy string
	// Key 推荐的 object key。客户端可以用也可以自己改（但不能越出 policy 限定的前缀）。
	Key string
	// PublicUrl 上传后可访问的地址。客户端上传完把它提交给业务接口落库。
	PublicUrl string
	// ExpireAt 整套凭证过期时间，Unix 秒。
	ExpireAt int64
	// SecurityToken STS 临时 token。
	SecurityToken string
	// Credential x-oss-credential 表单域的值。
	Credential string
	// DateTime x-oss-date 表单域的值。
	DateTime string
	// Signature x-oss-signature 表单域的值。
	Signature string
	// MaxSize 服务端强制的文件大小上限，字节。
	MaxSize int64
}

// PostUploadOptions 签发 PostObject 直传表单的入参。
type PostUploadOptions struct {
	// Dir key 前缀，末尾必须带斜杠，如 "avatar/abc123/"。
	//
	// 会写进 policy 的 starts-with 条件 —— 客户端不能传别的前缀，
	// 也就不能往别人的目录里写。
	Dir string
	// Ext 文件后缀，不带点，如 "png"。参与生成推荐 key。
	Ext string
	// AllowedExts 允许的后缀白名单。
	AllowedExts []string
	// MinSize 允许的最小字节数，小于等于 0 表示不限下界。
	MinSize int64
	// MaxSize 允许的最大字节数，**必须大于 0** —— 不填就没有服务端强制上限。
	MaxSize int64
	// ContentTypes 允许的 MIME 类型白名单，留空表示不限定。
	//
	// 写进 policy 的 in 条件 —— **OSS 拿文件自己的 Content-Type 去比**，
	// 不是比表单域。客户端改不了这个值：改了就跟 policy 对不上，
	// 或者压根传不上去。
	//
	// 注意浏览器 FormData 里 file 域的 Content-Type 是浏览器按文件后缀猜的
	// （.png → image/png，.jpg → image/jpeg）。白名单得按浏览器的猜测值写，
	// 不然正常上传会被拒。
	ContentTypes []string
	// TTL 凭证有效期（秒），小于等于 0 时用 DefaultPolicyExpiration。
	TTL int
}

// GeneratePostUploadForm 签发一次 V4 PostObject 直传表单。
// ctx 上下文
// opts 上传参数
//
// 后缀不在白名单内时返 ErrInvalidExt。
// MaxSize <= 0 时返 ErrInvalidMaxSize —— 不给上限就等于没开这个功能，
// 不该悄悄签出一个没有长度限制的表单。
func (c *Client) GeneratePostUploadForm(ctx context.Context, opts PostUploadOptions) (*PostUploadForm, error) {
	if err := ValidateExt(opts.Ext, opts.AllowedExts); err != nil {
		return nil, err
	}
	if opts.MaxSize <= 0 {
		return nil, ErrInvalidMaxSize
	}

	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultPolicyExpiration
	}

	dir := opts.Dir
	if dir == "" {
		dir = "/"
	}
	if dir[len(dir)-1] != '/' {
		dir += "/"
	}

	cred, err := c.fetchSTSCredential(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	date := now.Format("20060102")
	dateTime := now.Format("20060102T150405Z")
	credential := fmt.Sprintf("%s/%s/%s/%s/aliyun_v4_request",
		cred.AccessKeyId, date, c.cfg.Region, postProduct)

	// policy 的 conditions。
	// 顺序不影响校验结果，但固定顺序便于排查时对照解码后的 policy。
	conditions := []any{
		// bucket：只能往这一个 bucket 传。
		map[string]string{"bucket": c.cfg.Bucket},
		// 固定 V4 签名版本。
		map[string]string{"x-oss-signature-version": SignatureVersion},
		// 派生密钥的参数集。
		map[string]string{"x-oss-credential": credential},
		// 请求时间，ISO 8601；OSS 允许比这个时间晚最多 15 分钟收到。
		map[string]string{"x-oss-date": dateTime},
		// STS 临时凭证的 token。用对象形式声明，值是实际 token。
		map[string]string{"x-oss-security-token": cred.SecurityToken},
		// key 必须以 dir 开头 —— 防越权上传到别人的目录。
		[]any{"starts-with", "$key", dir},
		// 文件长度区间。**这是服务端强制上限的落点**，
		// 超限直接返 EntityTooLarge，文件根本不会落盘。
		[]any{"content-length-range", opts.MinSize, opts.MaxSize},
	}
	// MIME 类型限制。留空不写这条条件。
	//
	// 用 in 而不是 eq —— eq 只接受一个值，而头像有 jpeg/png/webp 好几种。
	if len(opts.ContentTypes) > 0 {
		conditions = append(conditions, []any{"in", "$content-type", opts.ContentTypes})
	}

	policyMap := map[string]any{
		"expiration": now.Add(time.Duration(ttl) * time.Second).Format("2006-01-02T15:04:05.000Z"),
		"conditions": conditions,
	}
	policyRaw, err := json.Marshal(policyMap)
	if err != nil {
		return nil, err
	}
	policy := base64.StdEncoding.EncodeToString(policyRaw)

	// V4 签名：4 次 HMAC-SHA256 派生 signing key，再签 base64 后的 policy。
	signingKey := c.derivePostSigningKey(cred.AccessKeySecret, date)
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(policy))
	signature := hex.EncodeToString(mac.Sum(nil))

	fileKey := dir + newObjectID() + "." + NormalizeExt(opts.Ext)

	return &PostUploadForm{
		Host:          c.cfg.uploadHost(),
		Policy:        policy,
		Key:           fileKey,
		PublicUrl:     c.URL(fileKey),
		ExpireAt:      now.Add(time.Duration(ttl) * time.Second).Unix(),
		SecurityToken: cred.SecurityToken,
		Credential:    credential,
		DateTime:      dateTime,
		Signature:     signature,
		MaxSize:       opts.MaxSize,
	}, nil
}

// derivePostSigningKey 派生 V4 signing key。
// secret STS 临时 SK
// date  日期，格式 20060102
//
// 4 级 HMAC-SHA256：aliyun_v4+secret → date → region → oss → aliyun_v4_request。
func (c *Client) derivePostSigningKey(secret, date string) []byte {
	newHash := func() hash.Hash { return sha256.New() }

	h1 := hmac.New(newHash, []byte("aliyun_v4"+secret))
	io.WriteString(h1, date)
	h2 := hmac.New(newHash, h1.Sum(nil))
	io.WriteString(h2, c.cfg.Region)
	h3 := hmac.New(newHash, h2.Sum(nil))
	io.WriteString(h3, postProduct)
	h4 := hmac.New(newHash, h3.Sum(nil))
	io.WriteString(h4, "aliyun_v4_request")
	return h4.Sum(nil)
}

// newObjectID 生成 object key 里的唯一标识。
func newObjectID() string {
	return uuid.NewString()
}
