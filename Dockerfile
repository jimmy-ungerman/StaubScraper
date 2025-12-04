# Base image with Selenium standalone Chromium
FROM selenium/standalone-chromium:latest

# Switch to root to install Python packages
USER root
WORKDIR /app

# Copy Python requirements
COPY requirements.txt .

# Install Python packages
RUN pip install --no-cache-dir -r requirements.txt

# Copy Flask app
COPY app.py .

# Expose Flask port
EXPOSE 5000

# Start both Selenium standalone and Flask app
CMD ["bash", "-c", "nohup /opt/bin/entry_point.sh & python /app/app.py"]