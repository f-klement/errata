"""The Larder API."""

import os

from routes import app

PORT = int(os.environ.get("API_PORT", "8080"))

if __name__ == "__main__":
    app.run(port=PORT)
