<?php

use App\Http\Controllers\UserController;

Route::get("/users/{id}", [UserController::class, "show"]);
