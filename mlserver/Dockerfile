FROM python:3.9-slim

# docker build -t ghcr.io/converged-computing/aws-performance-study:model-server .

WORKDIR /code
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
RUN mv ./models /models
EXPOSE 5000
CMD ["gunicorn", "--bind", "0.0.0.0:5000", "app:app"]
