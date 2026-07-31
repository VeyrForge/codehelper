from rest_framework.views import APIView


class ViewSet(APIView):
    def list(self, request):
        return []

    def retrieve(self, request, pk=None):
        return None


class DefaultRouter:
    def register(self, prefix, viewset, basename=None):
        return None
