extends EditorPlugin

# Vendor addon lifecycle — collides on bare `_ready` with scripts/player.gd.
func _ready() -> void:
	print("vendor addon ready")
