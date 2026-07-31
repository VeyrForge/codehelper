<?php

namespace App\Service;

class UserService
{
    public function find(int $id): array
    {
        return ["id" => $id, "name" => "probe"];
    }
}
