from rest_framework.viewsets import ViewSet


class UserService:
    @staticmethod
    def list_all():
        return []


class UserViewSet(ViewSet):
    def list(self, request):
        return UserService.list_all()
