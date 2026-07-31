# Minimal DRF-shaped APIView for paired probes (no Django install required).


class APIView:
    def dispatch(self, request, *args, **kwargs):
        return None

    @classmethod
    def as_view(cls, actions=None, **initkwargs):
        return cls()
