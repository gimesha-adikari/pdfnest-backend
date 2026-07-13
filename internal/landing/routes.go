package landing

import (
	"fmt"
	"os"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(app *fiber.App) {
	app.Get("/", landingPage)
}

func landingPage(c *fiber.Ctx) error {
	backendURL := os.Getenv("BACKEND_URL")
	if backendURL == "" {
		backendURL = "http://localhost:8080"
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>PDFNest Backend</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{
    font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
    background:#0f172a;
    color:#f8fafc;
    display:flex;
    justify-content:center;
    align-items:center;
    min-height:100vh;
    padding:2rem;
}
.container{
    max-width:760px;
    width:100%%;
    background:#111827;
    border:1px solid #334155;
    border-radius:16px;
    padding:40px;
}
h1{
    margin-bottom:16px;
    font-size:2rem;
}
p{
    color:#cbd5e1;
    line-height:1.7;
    margin-bottom:18px;
}
.card{
    background:#1e293b;
    padding:18px;
    border-radius:10px;
    margin-top:16px;
}
a{
    color:#60a5fa;
    text-decoration:none;
    word-break:break-all;
}
a:hover{
    text-decoration:underline;
}
footer{
    margin-top:30px;
    color:#94a3b8;
    font-size:.9rem;
}
</style>
</head>
<body>
<div class="container">
<h1>PDFNest Backend API</h1>

<p>
You're viewing the backend service of <strong>PDFNest</strong>.
This server powers authentication, PDF processing, OCR, document editing,
compression, conversion, security, and other API endpoints.
</p>

<p>
If you arrived here from the GitHub repository, the backend itself isn't meant
to be used directly through a browser. To experience PDFNest, visit the frontend
application below and explore all available PDF tools.
</p>

<div class="card">
<h3>Frontend Application</h3>
<p><a href="%s" target="_blank">%s</a></p>
</div>

<div class="card">
<h3>Backend API</h3>
<p><a href="%s" target="_blank">%s</a></p>
<p>Health Check: <a href="%s/health">%s/health</a></p>
</div>

<footer>
PDFNest Backend • Built with Go & Fiber
</footer>

</div>
</body>
</html>`,
		frontendURL,
		frontendURL,
		backendURL,
		backendURL,
		backendURL,
		backendURL,
	)

	c.Type("html")
	return c.SendString(html)
}
