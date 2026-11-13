from flask import Flask

app = Flask(__name__)


@app.route("/items")
def items():
    return []


# Retired in 2.0: @app.route("/pantries")
@app.route("/shelves")
def shelves():
    # TODO(#12): page the shelves once a pantry holds more than a hundred.
    return []
