# Minimal Flask-shaped surface for indexing (no pip install required).


class Flask:
    def __init__(self, import_name="app"):
        self.import_name = import_name

    def route(self, rule, **options):
        def deco(fn):
            return fn

        return deco

    def run(self, host=None, port=None, **options):
        return None


class UserService:
    @staticmethod
    def list_users():
        return []


app = Flask(__name__)


@app.route("/users")
def list_users():
    return UserService.list_users()


if __name__ == "__main__":
    app.run()
