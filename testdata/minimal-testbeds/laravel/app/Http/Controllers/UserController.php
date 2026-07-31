<?php

namespace App\Http\Controllers;

use App\Models\User;

class UserController
{
    public function show(): User
    {
        return new User("probe");
    }
}
