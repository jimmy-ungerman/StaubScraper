import os
from flask import Flask, jsonify
from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC

# Disable Selenium Manager BEFORE importing webdriver
os.environ["SELENIUM_MANAGER_DISABLED"] = "true"

app = Flask(__name__)

EMAIL = os.environ.get("EMAIL")
PASSWORD = os.environ.get("PASSWORD")
CHROMEDRIVER_PATH = "/usr/bin/chromedriver"  # Correct path in this image
SELENIUM_WAIT = 15  # seconds

def scrape_tanks():
    chrome_options = Options()
    chrome_options.add_argument("--headless=new")
    chrome_options.add_argument("--no-sandbox")
    chrome_options.add_argument("--disable-setuid-sandbox")
    chrome_options.add_argument("--disable-dev-shm-usage")
    chrome_options.add_argument("--disable-gpu")
    chrome_options.add_argument("--user-data-dir=/tmp/chrome-data")

    chrome_service = Service(executable_path=CHROMEDRIVER_PATH,log_path="/tmp/chromedriver.log",service_args=["--verbose"])
    driver = webdriver.Chrome(service=chrome_service, options=chrome_options)

    try:
        # --- 1. Login ---
        driver.get("https://edstaub.myfuelportal.com/Account/Login")

        WebDriverWait(driver, SELENIUM_WAIT).until(
            EC.presence_of_element_located((By.ID, "EmailAddress"))
        )

        driver.find_element(By.ID, "EmailAddress").send_keys(EMAIL)
        driver.find_element(By.ID, "Password").send_keys(PASSWORD)
        driver.find_element(By.CSS_SELECTOR, "button[type='submit']").click()

        WebDriverWait(driver, SELENIUM_WAIT).until(
            EC.presence_of_element_located((By.CSS_SELECTOR, "h3.box-title"))
        )

        # --- 2. Navigate to Tank page ---
        driver.get("https://edstaub.myfuelportal.com/Tank")

        WebDriverWait(driver, SELENIUM_WAIT).until(
            EC.presence_of_element_located((By.CSS_SELECTOR, "div.progress-bar"))
        )

        gallons_text = driver.find_element(
            By.XPATH, "//div[contains(text(),'Approximately') and contains(text(),'gallons')]"
        ).text
        gallons = int(''.join(filter(str.isdigit, gallons_text)))

        percent_text = driver.find_element(By.CSS_SELECTOR, "div.progress-bar").get_attribute("aria-valuenow")
        percent = float(percent_text)

        tank_name = driver.find_element(By.CSS_SELECTOR, "span.text-larger").text

        return [{"gallons": gallons, "percent": percent, "tank_name": tank_name}]

    finally:
        driver.quit()


@app.route("/tanks", methods=["GET"])
def tanks_endpoint():
    try:
        data = scrape_tanks()
        return jsonify(data)
    except Exception as e:
        return jsonify({"error": str(e)}), 500


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=5000)
