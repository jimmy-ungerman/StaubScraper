FROM selenium/standalone-chromium:142.0

USER root
WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .

RUN mkdir -p /dev/shm && chmod 777 /dev/shm

EXPOSE 5000

CMD ["bash", "-c", "nohup /opt/bin/entry_point.sh & python /app/app.py"]
