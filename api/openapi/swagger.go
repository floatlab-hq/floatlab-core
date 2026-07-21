package openapi

import (
	"embed"
	"io"
	"net/http"
)

//go:embed *.yaml
var specs embed.FS

const index = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>FloatLab Management API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>SwaggerUIBundle({url: "./openapi.yaml", dom_id: "#swagger-ui", deepLinking: true})</script>
</body>
</html>`

// Handler serves Swagger UI and the embedded management API specifications.
func Handler() http.Handler {
	files := http.FileServer(http.FS(specs))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, index)
			return
		}
		files.ServeHTTP(w, r)
	})
}
