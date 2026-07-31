extends Node

class_name Player

signal moved

func _ready() -> void:
	var foe = Enemy.new()
	foe.take_hit(1)
	var bar = %HealthBar as HealthBar
	bar.set_amount(100)
	moved.connect(_on_moved)
	moved.emit()

func move(delta: Vector2) -> void:
	position += delta
	moved.emit()

func _on_moved() -> void:
	pass
