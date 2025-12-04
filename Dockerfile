# Use Selenium standalone Chromium image
FROM selenium/standalone-chromium:142.0

USER root
WORKDIR /app

# Copy requirements and install Python packages
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy Flask app
COPY app.py .

# Expose Flask port
EXPOSE 5000

# Run Flask app; Selenium server is already running in this image
CMD ["python", "app.py"]
