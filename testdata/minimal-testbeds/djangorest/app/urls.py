from rest_framework.viewsets import DefaultRouter

from app.views import UserViewSet

router = DefaultRouter()
router.register("users", UserViewSet)

urlpatterns = [
    path("users/", UserViewSet.as_view({"get": "list"})),
]
