# PDFNest Render Deploy

## What to commit
- Dockerfile
- .dockerignore
- requirements.txt
- render.yaml
- your updated main.go

## Important runtime notes
- Go binary is built in a separate build stage.
- Python helper scripts run from /app/scripts.
- The image includes Ghostscript, Tesseract, Chromium, and the Python libraries used by your PDF scripts.

## Render setup
1. Push to GitHub.
2. Create a new Render Web Service.
3. Choose Docker runtime.
4. Add environment variables.
5. Deploy.

## Recommended environment variables
- PORT=10000
- ALLOWED_ORIGINS=https://your-frontend-domain.com
- DATABASE_URL=...
- JWT_SECRET=...
- Any auth/payment/provider keys you use

## Health check
Set your backend to answer on /api/health if possible.
If you do not have one, either add it or change healthCheckPath in render.yaml.