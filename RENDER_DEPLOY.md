# Platen PDF Backend - Render Deployment

## What to commit

- Dockerfile
- Dockerfile.dev (optional, for local development)
- .dockerignore
- render.yaml
- Your updated Go backend source

> The Go backend no longer contains or executes any Python scripts. All PDF processing is handled by the separate FastAPI worker service.

---

## Runtime notes

- The Go application is built in a separate builder stage.
- The runtime image contains only the Go server and its required system dependencies.
- The backend communicates with the Platen PDF Worker over HTTP for PDF processing.
- Chromium is included only if your backend still uses `chromedp` or performs HTML-to-PDF conversion. If not, it can be removed.

---

## Render setup

1. Push the repository to GitHub.
2. Create a new **Render Web Service**.
3. Select **Docker** as the runtime.
4. Configure the required environment variables.
5. Deploy.

---

## Required environment variables

- `PORT=10000`
- `WORKER_URL=https://your-worker-service.onrender.com`
- `ALLOWED_ORIGINS=https://your-frontend-domain.com`
- `DATABASE_URL=...`
- `JWT_SECRET=...`

Add any additional authentication, payment, email, or third-party provider keys your backend requires.

---

## Worker deployment

The FastAPI worker is a separate service and must be deployed independently.

The Go backend expects it to be reachable through the `WORKER_URL` environment variable.

---

## Health check

Configure Render to use:

```
/api/health
```

If your backend uses a different health endpoint, update `healthCheckPath` in `render.yaml` accordingly.