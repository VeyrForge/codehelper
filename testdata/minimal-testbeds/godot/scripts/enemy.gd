extends Node2D

class_name Enemy

# ParentID from class_name; Player._ready calls take_hit for impact densify.
func _ready() -> void:
	pass

func take_hit(amount: int) -> void:
	pass
