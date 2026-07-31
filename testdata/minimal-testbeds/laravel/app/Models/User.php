<?php

namespace App\Models;

use Illuminate\Foundation\Auth\User as Authenticatable;
use Illuminate\Database\Eloquent\Factories\HasFactory;

class User extends Authenticatable
{
    use HasFactory;

    public string $name = "";

    public function __construct(string $name = "")
    {
        $this->name = $name;
    }

    public function id(): int
    {
        return $this->loadProfile();
    }

    public function loadProfile(): int
    {
        return 1;
    }

    public function toArray(): array
    {
        return ["name" => $this->name];
    }
}
