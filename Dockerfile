FROM python:3.13-slim

WORKDIR /app

COPY pyproject.toml uv.lock ./
RUN pip install --no-cache-dir uv && uv sync --frozen --no-dev

COPY kvstore/ kvstore/
COPY run_node.py run_proxy.py ./

ENV PYTHONUNBUFFERED=1

ENTRYPOINT ["uv", "run"]
