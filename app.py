import os
import time
import re
from flask import Flask, jsonify
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from bs4 import BeautifulSoup

EMAIL = os.getenv("EMAIL")
PASSWORD = os.getenv("PASSWORD")

if not EMAIL or not PASSWORD:
    raise Exception("Please set EMAIL and PASSWORD environment variables")

app = Flask(__name__)

CACHE = None
CACHE_TIMESTAMP = 0
CACHE_TTL = 6 * 60 * 60

SELENIUM_REMOTE_URL = "http://localhost:4444/wd/hub"

def scrape_tanks():
    global CACHE, CACHE_TIMESTAMP

    if CACHE and (time.time() - CACHE_TIMESTAMP) < CACHE_TTL:
        return CACHE

    chrome_options = Options()
    chrome_options.add_argument("--headless=new")
    chrome_options.add_argument("--no-sandbox")
    chrome_options.add_argument("--disable-dev-shm-usage")
    chrome_options.add_argument("--disable-gpu")
    chrome_options.add_argument("--disable-software-rasterizer")
    chrome_options.add_argument("--window-size=1920,1080")

    driver = webdriver.Remote(
        command_executor=SELENIUM_REMOTE_URL,
        options=chrome_options
    )

    try:
        driver.get("https://edstaub.myfuelportal.com/Account/Login")
        WebDriverWait(driver, 10).until(EC.presence_of_element_located((By.NAME, "EmailAddress")))

        driver.find_element(By.NAME, "EmailAddress").send_keys(EMAIL)
        driver.find_element(By.NAME, "Password").send_keys(PASSWORD)
        driver.find_element(By.XPATH, "//button[@type='submit']").click()

        WebDriverWait(driver, 10).until(
            EC.presence_of_element_located((By.XPATH, "//h3[text()='Account Details']"))
        )

        driver.get("https://edstaub.myfuelportal.com/Tank")
        WebDriverWait(driver, 10).until(
            EC.presence_of_all_elements_located((By.CLASS_NAME, "tank-row"))
        )

        soup = BeautifulSoup(driver.page_source, "html.parser")
        tanks = []

        for row in soup.select("div.tank-row"):
            name_span = row.select_one("div.col-sm-6.col-md-3 span.text-larger")
            tank_name = name_span.get_text(strip=True) if name_span else "unknown"

            progress = row.select_one("div.progress-bar")
            percent_value = float(progress.get("aria-valuenow")) if progress else 0.0

            gallons_text_div = row.select_one("div.progress + div")
            gallons = 0
            if gallons_text_div:
                match = re.search(r"(\d+)", gallons_text_div.get_text())
                if match:
                    gallons = int(match.group(1))

            tanks.append({
                "tank_name": tank_name,
                "percent": percent_value,
                "gallons": gallons
            })

        CACHE = tanks
        CACHE_TIMESTAMP = time.time()
        return tanks
    finally:
        driver.quit()

@app.route("/tanks", methods=["GET"])
def tanks_endpoint():
    return jsonify(scrape_tanks())

if __name__ == "__main__":
    app.run(host="0.0.0.0", port=5000)
