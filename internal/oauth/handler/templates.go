// templates.go 内联 HTML 模板:consent / login / error。
//
// 为什么内联而不是 LoadHTMLGlob:
//   - oauth 模块自包含,不依赖外部文件路径(容器里少一层挂载/拷贝)
//   - 模板数量少(3 个),内联更方便审阅"这个页面 render 了什么"
//   - html/template 自动转义 {{ . }},XSS 在 template 层已解决 —— 业务代码不用自己 escape
//
// 样式:MVP 走系统默认,只保证能用。未来要品牌化,再拆外部文件 + loadglob。
package handler

import "html/template"

// consentData consent 页渲染上下文。
type consentData struct {
	ClientName string
	Orgs       []OrgSummary
	Scope      string
	// 所有 OAuth 参数作为 hidden 字段保留,POST 时回写
	ClientID            string
	RedirectURI         string
	ResponseType        string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// loginData 登录页渲染上下文。ReturnTo 编码为 URL 安全字符串,form submit 时原样回传。
type loginData struct {
	ReturnTo string
	Error    string // 非空 = 上次登录失败
}

// errorData 通用错误页。Title 放粗大字,Detail 展示给终端用户,绝不 leak 内部栈。
type errorData struct {
	Title  string
	Detail string
}

var consentTpl = template.Must(template.New("consent").Parse(`<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>Authorize {{.ClientName}} - Synapse</title>
<style>
  body { font-family: -apple-system, sans-serif; max-width: 480px; margin: 4em auto; padding: 0 1em; color: #222; }
  h1 { font-size: 1.4em; }
  .app { padding: 1em; background: #f6f7f9; border-radius: 8px; margin: 1em 0; }
  label { display: block; margin: 1em 0 0.4em; font-weight: 600; }
  select { width: 100%; padding: 0.6em; font-size: 1em; }
  .buttons { margin-top: 2em; display: flex; gap: 1em; }
  button { flex: 1; padding: 0.8em; font-size: 1em; border-radius: 6px; border: 0; cursor: pointer; }
  .allow { background: #2563eb; color: white; }
  .deny { background: #e5e7eb; color: #111; }
  .scope { font-size: 0.9em; color: #555; }
</style>
</head>
<body>
<h1>Authorize application</h1>
<div class="app"><strong>{{.ClientName}}</strong> is requesting access to your Synapse organization.</div>
<p class="scope">Requested scope: <code>{{.Scope}}</code></p>
<form method="POST" action="/oauth/authorize">
  <label for="org_id">Grant access to organization:</label>
  <select name="org_id" id="org_id" required>
    {{range .Orgs}}<option value="{{.ID}}">{{.DisplayName}} ({{.Slug}})</option>{{end}}
  </select>
  <input type="hidden" name="client_id" value="{{.ClientID}}">
  <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
  <input type="hidden" name="response_type" value="{{.ResponseType}}">
  <input type="hidden" name="scope" value="{{.Scope}}">
  <input type="hidden" name="state" value="{{.State}}">
  <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
  <input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
  <div class="buttons">
    <button type="submit" name="action" value="deny" class="deny">Deny</button>
    <button type="submit" name="action" value="allow" class="allow">Allow</button>
  </div>
</form>
</body>
</html>`))

var loginTpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>Sign in - Synapse</title>
<style>
  body { font-family: -apple-system, sans-serif; max-width: 380px; margin: 4em auto; padding: 0 1em; color: #222; }
  h1 { font-size: 1.4em; }
  label { display: block; margin: 1em 0 0.4em; font-weight: 600; }
  input[type=email], input[type=password] { width: 100%; padding: 0.6em; font-size: 1em; box-sizing: border-box; }
  button { width: 100%; margin-top: 1.5em; padding: 0.8em; font-size: 1em; border-radius: 6px; border: 0; background: #2563eb; color: white; cursor: pointer; }
  .err { color: #b91c1c; margin-top: 1em; }
</style>
</head>
<body>
<h1>Sign in to continue</h1>
<p>An application is requesting access to your Synapse organization.</p>
<form method="POST" action="/oauth/login">
  <label for="email">Email</label>
  <input type="email" name="email" id="email" required autocomplete="email" autofocus>
  <label for="password">Password</label>
  <input type="password" name="password" id="password" required autocomplete="current-password">
  <input type="hidden" name="return_to" value="{{.ReturnTo}}">
  <button type="submit">Sign in</button>
  {{if .Error}}<p class="err">{{.Error}}</p>{{end}}
</form>
</body>
</html>`))

var errorTpl = template.Must(template.New("error").Parse(`<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>{{.Title}} - Synapse</title>
<style>
  body { font-family: -apple-system, sans-serif; max-width: 480px; margin: 4em auto; padding: 0 1em; color: #222; }
  h1 { font-size: 1.4em; color: #b91c1c; }
  .detail { padding: 1em; background: #f6f7f9; border-radius: 8px; font-family: monospace; font-size: 0.9em; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<div class="detail">{{.Detail}}</div>
<p><small>You can close this window.</small></p>
</body>
</html>`))
