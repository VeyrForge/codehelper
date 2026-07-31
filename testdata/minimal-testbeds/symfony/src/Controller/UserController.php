<?php

namespace App\Controller;

use App\Service\UserService;
use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\Routing\Attribute\Route;

class UserController extends AbstractController
{
    public function __construct(private UserService $users)
    {
    }

    #[Route("/users/{id}", name: "user_show", methods: ["GET"])]
    public function show(int $id): array
    {
        return $this->users->find($id);
    }
}
