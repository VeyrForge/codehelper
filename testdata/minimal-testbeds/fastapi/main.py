# Minimal FastAPI-shaped surface for indexing (no pip install required).


class FastAPI:
    def get(self, path, **options):
        def deco(fn):
            return fn

        return deco

    def include_router(self, router):
        return router


class APIRouter:
    def get(self, path, **options):
        def deco(fn):
            return fn

        return deco


def Depends(dependency=None, **options):
    return dependency


class UserService:
    @staticmethod
    def list_users(db):
        return []


app = FastAPI()
router = APIRouter()


def get_db():
    return {}


@app.get("/users")
def list_users(db=Depends(get_db)):
    return UserService.list_users(db)


@router.get("/items")
def list_items():
    return []


app.include_router(router)
