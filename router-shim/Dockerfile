FROM python:3.12-slim

WORKDIR /app

# Single-step install — keeps the image small. httpx[http2] for HTTP/2 streaming,
# starlette+uvicorn for ASGI. No app deps beyond these three.
RUN pip install --no-cache-dir \
    "httpx[http2]==0.28.1" \
    "starlette==0.41.3" \
    "uvicorn[standard]==0.32.1"

COPY app.py /app/app.py

ENV LISTEN_HOST=0.0.0.0 \
    LISTEN_PORT=8000 \
    UPSTREAM_BASE_URL=http://cli-proxy-api:8317 \
    PYTHONUNBUFFERED=1

EXPOSE 8000

HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
    CMD python -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8000/_shim/health',timeout=2).status==200 else 1)"

CMD ["python", "-m", "uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000", "--no-access-log"]
